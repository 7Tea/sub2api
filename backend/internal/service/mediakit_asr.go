package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	MediaKitASRModel          = "volcengine-asr-subtitles"
	mediaKitASRHost           = "mediakit.cn-beijing.volces.com"
	mediaKitASRBasePath       = "/api/v1"
	mediaKitASRSubmitPath     = "/tools/asr-subtitles"
	mediaKitASRTaskPath       = "/tasks/"
	mediaKitASRTaskBindingTTL = 24 * time.Hour
)

func isMediaKitASRAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(account.GetOpenAIBaseURL()))
	return err == nil && strings.EqualFold(u.Scheme, "https") && strings.EqualFold(u.Hostname(), mediaKitASRHost)
}

func MediaKitASRRequestSessionHash(taskID string, userID, apiKeyID int64) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || userID <= 0 || apiKeyID <= 0 {
		return ""
	}
	return "mediakit-asr:" + DeriveSessionHashFromSeed(fmt.Sprintf("%d:%d:%s", userID, apiKeyID, taskID))
}

func (s *OpenAIGatewayService) BindMediaKitASRRequestAccount(
	ctx context.Context,
	groupID *int64,
	taskID string,
	userID, apiKeyID, accountID int64,
) error {
	if s == nil || s.cache == nil {
		return fmt.Errorf("MediaKit ASR task binding cache is unavailable")
	}
	sessionHash := MediaKitASRRequestSessionHash(taskID, userID, apiKeyID)
	cacheKey := s.openAISessionCacheKey(sessionHash)
	if cacheKey == "" || accountID <= 0 {
		return fmt.Errorf("MediaKit ASR task binding is invalid")
	}
	return s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), cacheKey, accountID, mediaKitASRTaskBindingTTL)
}

func (s *OpenAIGatewayService) ResolveMediaKitASRRequestAccount(
	ctx context.Context,
	groupID *int64,
	taskID string,
	userID, apiKeyID int64,
) (int64, error) {
	if s == nil || s.cache == nil {
		return 0, fmt.Errorf("MediaKit ASR task binding cache is unavailable")
	}
	cacheKey := s.openAISessionCacheKey(MediaKitASRRequestSessionHash(taskID, userID, apiKeyID))
	if cacheKey == "" {
		return 0, fmt.Errorf("MediaKit ASR task binding is invalid")
	}
	return s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), cacheKey)
}

type MediaKitASRForwardResponse struct {
	Success bool            `json:"success"`
	TaskID  string          `json:"task_id"`
	Status  string          `json:"status"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (s *OpenAIGatewayService) ForwardMediaKitASR(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	taskID string,
	body []byte,
) ([]byte, error) {
	if s == nil || account == nil || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityMediaKitASR) {
		return nil, fmt.Errorf("MediaKit ASR requires an enabled API-key account")
	}
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return nil, fmt.Errorf("MediaKit ASR account is missing api_key")
	}
	baseURL, err := mediaKitASRBaseURL(account)
	if err != nil {
		return nil, err
	}
	method := http.MethodGet
	requestURL := baseURL + mediaKitASRTaskPath + url.PathEscape(strings.TrimSpace(taskID))
	var requestBody []byte
	if taskID == "" {
		payload, err := buildMediaKitASRSubmitPayload(body)
		if err != nil {
			return nil, err
		}
		requestBody, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode MediaKit ASR request: %w", err)
		}
		method = http.MethodPost
		requestURL = baseURL + mediaKitASRSubmitPath
	}

	upstreamCtx, release := detachUpstreamContext(ctx)
	defer release()
	request, err := http.NewRequestWithContext(upstreamCtx, method, requestURL, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("build MediaKit ASR request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	SetActualOpenAIUpstreamEndpoint(c, request.URL.Path)
	SetOpsUpstreamModel(c, MediaKitASRModel)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	response, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("MediaKit ASR request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := ReadUpstreamResponseBody(response.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("MediaKit ASR upstream returned %d: %s", response.StatusCode, sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(responseBody)))
	}
	return responseBody, nil
}

func mediaKitASRBaseURL(account *Account) (string, error) {
	u, err := url.Parse(strings.TrimSpace(account.GetOpenAIBaseURL()))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), mediaKitASRHost) ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != mediaKitASRBasePath {
		return "", fmt.Errorf("MediaKit ASR account must use https://%s%s", mediaKitASRHost, mediaKitASRBasePath)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func buildMediaKitASRSubmitPayload(body []byte) (map[string]any, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model != MediaKitASRModel {
		return nil, fmt.Errorf("model must be %q", MediaKitASRModel)
	}
	audioURL := strings.TrimSpace(gjson.GetBytes(body, "audio_url").String())
	videoURL := strings.TrimSpace(gjson.GetBytes(body, "video_url").String())
	if audioURL == "" && videoURL == "" {
		return nil, fmt.Errorf("audio_url or video_url is required")
	}
	payload := map[string]any{
		"client_token": uuid.NewString(),
		"content_type": "speech",
		"language":     "cmn-Hans-CN",
	}
	for key, value := range map[string]string{
		"audio_url": audioURL, "video_url": videoURL,
		"content_type":  strings.TrimSpace(gjson.GetBytes(body, "content_type").String()),
		"language":      strings.TrimSpace(gjson.GetBytes(body, "language").String()),
		"callback_args": strings.TrimSpace(gjson.GetBytes(body, "callback_args").String()),
		"queue_id":      strings.TrimSpace(gjson.GetBytes(body, "queue_id").String()),
	} {
		if value != "" {
			payload[key] = value
		}
	}
	for _, key := range []string{"enable_speaker_info", "enable_confidence"} {
		value := gjson.GetBytes(body, key)
		if value.Type == gjson.True || value.Type == gjson.False {
			payload[key] = value.Bool()
		}
	}
	if gjson.GetBytes(body, "client_token").Exists() {
		value := strings.TrimSpace(gjson.GetBytes(body, "client_token").String())
		if value == "" {
			return nil, fmt.Errorf("client_token cannot be empty")
		}
		payload["client_token"] = value
	}
	return payload, nil
}

func MediaKitASRStatus(response MediaKitASRForwardResponse) string {
	if !response.Success && len(response.Error) > 0 && string(response.Error) != "null" {
		return "failed"
	}
	status := strings.ToLower(strings.TrimSpace(response.Status))
	switch status {
	case "completed", "succeeded", "success":
		return "succeeded"
	case "failed", "error", "canceled", "cancelled":
		return "failed"
	case "queued", "pending":
		return "queued"
	case "running", "processing", "":
		if len(response.Result) > 0 && string(response.Result) != "null" {
			return "succeeded"
		}
		return "running"
	default:
		return "running"
	}
}

func MediaKitASRDurationSeconds(result json.RawMessage) float64 {
	if len(result) == 0 {
		return 0
	}
	for _, path := range []string{"audio_info.duration_seconds", "duration_seconds", "audio_info.duration_ms", "duration_ms"} {
		value := gjson.GetBytes(result, path)
		if value.Type == gjson.Number && value.Float() > 0 {
			seconds := value.Float()
			if strings.HasSuffix(path, "duration_ms") {
				seconds /= 1000
			}
			return seconds
		}
	}
	for _, path := range []string{"audio_info.duration", "duration"} {
		if value := gjson.GetBytes(result, path); value.Type == gjson.Number && value.Float() > 0 {
			seconds := value.Float()
			if seconds > 1800 {
				seconds /= 1000
			}
			return seconds
		}
	}
	var maxEnd float64
	for _, subtitle := range gjson.GetBytes(result, "subtitles").Array() {
		if end := subtitle.Get("end_time").Float(); end > maxEnd {
			maxEnd = end
		}
	}
	return maxEnd
}
