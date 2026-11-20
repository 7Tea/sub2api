package service

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	HeyGenVideoModel = "heygen-avatar-video"
)

// ForwardHeyGenVideo passes the v3 image/avatar video payload through and uses
// the task ID path to retrieve asynchronous results. Image/audio URLs or
// previously uploaded HeyGen asset IDs are supported by the upstream contract.
func (s *OpenAIGatewayService) ForwardHeyGenVideo(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	taskID string,
	body []byte,
) ([]byte, *OpenAIForwardResult, error) {
	if s == nil || account == nil || account.Platform != PlatformHeyGen || account.Type != AccountTypeAPIKey {
		return nil, nil, fmt.Errorf("HeyGen requires an API-key account on the HeyGen platform")
	}
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return nil, nil, fmt.Errorf("HeyGen account is missing api_key")
	}
	baseURL, err := heyGenBaseURL(account)
	if err != nil {
		return nil, nil, err
	}
	method := http.MethodGet
	path := "/videos/" + url.PathEscape(strings.TrimSpace(taskID))
	var requestBody []byte
	model := HeyGenVideoModel
	if taskID == "" {
		if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
			return nil, nil, fmt.Errorf("request body must be a JSON object")
		}
		if gjson.GetBytes(body, "type").String() != "image" {
			return nil, nil, fmt.Errorf("HeyGen currently supports type=image video requests")
		}
		if strings.TrimSpace(gjson.GetBytes(body, "model").String()) != HeyGenVideoModel {
			return nil, nil, fmt.Errorf("model must be %q", HeyGenVideoModel)
		}
		imageType := gjson.GetBytes(body, "image.type").String()
		if imageType == "" || (imageType != "url" && imageType != "asset_id") {
			return nil, nil, fmt.Errorf("image.type must be url or asset_id")
		}
		if imageType == "url" && strings.TrimSpace(gjson.GetBytes(body, "image.url").String()) == "" {
			return nil, nil, fmt.Errorf("image.url is required")
		}
		if imageType == "asset_id" && strings.TrimSpace(gjson.GetBytes(body, "image.asset_id").String()) == "" {
			return nil, nil, fmt.Errorf("image.asset_id is required")
		}
		if strings.TrimSpace(gjson.GetBytes(body, "audio_url").String()) == "" &&
			strings.TrimSpace(gjson.GetBytes(body, "audio_asset_id").String()) == "" {
			return nil, nil, fmt.Errorf("audio_url or audio_asset_id is required")
		}
		method = http.MethodPost
		path = "/videos"
		cleanedBody, err := sjson.DeleteBytes(body, "model")
		if err != nil {
			return nil, nil, fmt.Errorf("remove gateway model field: %w", err)
		}
		requestBody = cleanedBody
	}

	upstreamCtx, release := detachUpstreamContext(ctx)
	defer release()
	request, err := http.NewRequestWithContext(upstreamCtx, method, baseURL+path, bytes.NewReader(requestBody))
	if err != nil {
		return nil, nil, fmt.Errorf("build HeyGen request: %w", err)
	}
	request.Header.Set("x-api-key", apiKey)
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	SetActualOpenAIUpstreamEndpoint(c, "/v3"+path)
	SetOpsUpstreamModel(c, model)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	response, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, nil, fmt.Errorf("HeyGen request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := ReadUpstreamResponseBody(response.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("HeyGen returned %d: %s", response.StatusCode, sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(responseBody)))
	}
	result := &OpenAIForwardResult{
		Model: model, BillingModel: model, UpstreamModel: model,
		RequestID:       firstNonEmpty(gjson.GetBytes(responseBody, "data.video_id").String(), gjson.GetBytes(responseBody, "data.id").String(), uuid.NewString()),
		ResponseHeaders: response.Header.Clone(), Duration: time.Since(started),
	}
	if method == http.MethodPost {
		result.ResponseID = firstNonEmpty(gjson.GetBytes(responseBody, "data.video_id").String(), gjson.GetBytes(responseBody, "data.id").String())
		if result.ResponseID == "" {
			return nil, nil, fmt.Errorf("HeyGen create response is missing video_id")
		}
	}
	return responseBody, result, nil
}

// HeyGen credentials belong to their own platform; GetOpenAIBaseURL deliberately
// rejects platforms that do not implement OpenAI's inference protocol.
func heyGenBaseURL(account *Account) (string, error) {
	if account == nil || account.Platform != PlatformHeyGen || account.Type != AccountTypeAPIKey {
		return "", fmt.Errorf("HeyGen requires an API-key account on the HeyGen platform")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/")
	base, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(base.Scheme, "https") || !strings.EqualFold(base.Host, "api.heygen.com") || base.Path != "/v3" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("HeyGen account base URL must be https://api.heygen.com/v3")
	}
	return "https://api.heygen.com/v3", nil
}

// HasHeyGenVideoPricing admits the same per-model video cards used by RecordUsage.
// Keep legacy flat group prices usable while operators move to model pricing.
func (s *OpenAIGatewayService) HasHeyGenVideoPricing(ctx context.Context, key *APIKey, resolution string) bool {
	if s == nil || key == nil || key.Group == nil {
		return false
	}
	resolved := s.resolveOpenAIChannelPricing(ctx, HeyGenVideoModel, key)
	if resolved != nil {
		// Token cards cannot price HeyGen's duration-only usage. Group cards
		// override legacy prices, so a wrong/empty group card must not pass.
		if resolved.Mode == BillingModeToken || resolved.Source == PricingSourceGroup {
			return configuredHeyGenVideoTier(resolved, resolution)
		}
		if key.Group.GetVideoPriceForModel(HeyGenVideoModel, resolution) == nil {
			return configuredHeyGenVideoTier(resolved, resolution)
		}
	}
	return key.Group.GetVideoPriceForModel(HeyGenVideoModel, resolution) != nil
}

func configuredHeyGenVideoTier(resolved *ResolvedPricing, resolution string) bool {
	if resolved.Mode != BillingModeVideo || resolved.channelPricing == nil {
		return false
	}
	for _, tier := range resolved.RequestTiers {
		if tier.TierLabel == resolution && tier.PerRequestPrice != nil {
			return true
		}
	}
	return resolved.channelPricing.PerRequestPrice != nil
}

func HeyGenVideoStatus(body []byte) (status string, durationSeconds int, resolution string) {
	status = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "data.status").String()))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "status").String()))
	}
	if status == "completed" || status == "complete" || status == "succeeded" {
		status = "completed"
	} else if status == "failed" || status == "error" {
		status = "failed"
	} else {
		status = "processing"
	}
	durationSeconds = int(math.Ceil(gjson.GetBytes(body, "data.duration").Float()))
	if durationSeconds <= 0 {
		durationSeconds = int(math.Ceil(gjson.GetBytes(body, "data.video.duration").Float()))
	}
	resolution = strings.TrimSpace(gjson.GetBytes(body, "data.resolution").String())
	return status, durationSeconds, resolution
}
