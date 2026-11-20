package service

import (
	"bytes"
	"context"
	"fmt"
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
	tokenHubMiniMaxTTSEndpoint   = "/v1/wand/minimax-tts/sync_tts"
	tokenHubMiniMaxCloneEndpoint = "/v1/wand/minimax-tts/sync_clone"
)

var tokenHubMiniMaxTTSModels = map[string]struct{}{
	"minimax-speech-2.8-hd":    {},
	"minimax-speech-2.8-turbo": {},
	"minimax-speech-2.6-hd":    {},
	"minimax-speech-2.6-turbo": {},
	"minimax-speech-02-hd":     {},
	"minimax-speech-02-turbo":  {},
}

func isTokenHubSpeechAccount(account *Account) bool {
	return isTokenHubImageAccount(account)
}

func isTokenHubMiniMaxTTSModel(model string) bool {
	_, ok := tokenHubMiniMaxTTSModels[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

func (s *OpenAIGatewayService) ForwardTokenHubMiniMaxSpeech(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	operation string,
	body []byte,
) ([]byte, *OpenAIForwardResult, error) {
	if s == nil || account == nil || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityTokenHubSpeech) {
		return nil, nil, fmt.Errorf("TokenHub MiniMax speech requires an enabled API-key account")
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, nil, fmt.Errorf("request body must be a JSON object")
	}

	requestModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	upstreamModel := requestModel
	endpoint := tokenHubMiniMaxTTSEndpoint
	encodedBody := body
	switch operation {
	case "tts":
		upstreamModel = strings.TrimSpace(account.GetMappedModel(requestModel))
		if !isTokenHubMiniMaxTTSModel(upstreamModel) {
			return nil, nil, fmt.Errorf("unsupported TokenHub MiniMax TTS model %q", requestModel)
		}
		if gjson.GetBytes(body, "stream").Bool() {
			return nil, nil, fmt.Errorf("streaming TokenHub TTS is not enabled in this adapter; use synchronous mode")
		}
		if strings.TrimSpace(gjson.GetBytes(body, "text").String()) == "" {
			return nil, nil, fmt.Errorf("text is required")
		}
		if upstreamModel != requestModel {
			var err error
			encodedBody, err = sjson.SetBytes(body, "model", upstreamModel)
			if err != nil {
				return nil, nil, fmt.Errorf("map TokenHub MiniMax TTS model: %w", err)
			}
		}
	case "clone":
		if requestModel != "minimax-voice-clone" {
			return nil, nil, fmt.Errorf("model must be %q", "minimax-voice-clone")
		}
		if strings.TrimSpace(gjson.GetBytes(body, "audio_url").String()) == "" &&
			strings.TrimSpace(gjson.GetBytes(body, "audio_data").String()) == "" && !gjson.GetBytes(body, "file_id").Exists() {
			return nil, nil, fmt.Errorf("audio_url, audio_data, or file_id is required")
		}
		endpoint = tokenHubMiniMaxCloneEndpoint
		upstreamModel = strings.TrimSpace(gjson.GetBytes(body, "tts_model").String())
		if upstreamModel != "" {
			upstreamModel = strings.TrimSpace(account.GetMappedModel(upstreamModel))
			if !isTokenHubMiniMaxTTSModel(upstreamModel) {
				return nil, nil, fmt.Errorf("unsupported TokenHub MiniMax TTS model %q", upstreamModel)
			}
			var err error
			encodedBody, err = sjson.SetBytes(body, "tts_model", upstreamModel)
			if err != nil {
				return nil, nil, fmt.Errorf("map TokenHub MiniMax clone preview model: %w", err)
			}
		}
	default:
		return nil, nil, fmt.Errorf("unsupported TokenHub MiniMax speech operation %q", operation)
	}

	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return nil, nil, fmt.Errorf("TokenHub speech account is missing api_key")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(account.GetOpenAIBaseURL()), "/")
	parsedBase, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(parsedBase.Scheme, "https") || !strings.EqualFold(parsedBase.Hostname(), "tokenhub.tencentmaas.com") || parsedBase.User != nil || parsedBase.RawQuery != "" || parsedBase.Fragment != "" {
		return nil, nil, fmt.Errorf("TokenHub speech account must use https://tokenhub.tencentmaas.com")
	}
	upstreamCtx, release := detachUpstreamContext(ctx)
	defer release()
	request, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, "https://tokenhub.tencentmaas.com"+endpoint, bytes.NewReader(encodedBody))
	if err != nil {
		return nil, nil, fmt.Errorf("build TokenHub MiniMax speech request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	SetActualOpenAIUpstreamEndpoint(c, endpoint)
	SetOpsUpstreamModel(c, firstNonEmpty(upstreamModel, requestModel))
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	response, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, nil, fmt.Errorf("TokenHub MiniMax speech request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := ReadUpstreamResponseBody(response.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := firstNonEmpty(gjson.GetBytes(responseBody, "base_resp.status_msg").String(), extractUpstreamErrorMessage(responseBody), http.StatusText(response.StatusCode))
		return nil, nil, fmt.Errorf("TokenHub MiniMax speech upstream returned %d: %s", response.StatusCode, sanitizeUpstreamErrorMessage(message))
	}
	baseRespCode := gjson.GetBytes(responseBody, "base_resp.status_code")
	if baseRespCode.Exists() && baseRespCode.Int() != 0 {
		message := firstNonEmpty(gjson.GetBytes(responseBody, "base_resp.status_msg").String(), "TokenHub MiniMax speech request failed")
		return nil, nil, fmt.Errorf("TokenHub MiniMax speech error %d: %s", baseRespCode.Int(), sanitizeUpstreamErrorMessage(message))
	}
	resultModel := firstNonEmpty(requestModel, upstreamModel)
	if operation == "clone" {
		if strings.TrimSpace(gjson.GetBytes(responseBody, "voice_id").String()) == "" {
			return nil, nil, fmt.Errorf("TokenHub clone response is missing voice_id")
		}
		resultModel = "minimax-voice-clone"
	}
	result := &OpenAIForwardResult{
		RequestID: firstNonEmpty(
			gjson.GetBytes(responseBody, "request_id").String(),
			gjson.GetBytes(responseBody, "trace_id").String(),
			gjson.GetBytes(responseBody, "id").String(),
			uuid.NewString(),
		),
		Model: resultModel, BillingModel: resultModel, UpstreamModel: firstNonEmpty(upstreamModel, resultModel),
		Duration: time.Since(started),
	}
	characters := max(
		int(gjson.GetBytes(responseBody, "usage.chars").Int()),
		int(gjson.GetBytes(responseBody, "extra_info.usage_characters").Int()),
	)
	if operation == "clone" {
		// The caller bought one successful clone operation. Its local price
		// is independent of upstream activation timing and TTS characters.
		result.RequestID = StableGrokAudioBillingRequestID("tokenhub-clone:" + result.RequestID)
		result.AudioUsage = &AudioUsage{Mode: "clone", DurationOrUnits: 1}
	} else if characters > 0 {
		result.Usage.OutputTokens = max(0, int(gjson.GetBytes(responseBody, "usage.total_token").Int()))
		result.AudioUsage = &AudioUsage{Mode: "tts", DurationOrUnits: float64(characters) / 1_000_000}
	}
	return responseBody, result, nil
}

// TokenHubVoiceClonePrice reuses the existing model card, never TTS defaults.
func (s *OpenAIGatewayService) TokenHubVoiceClonePrice(ctx context.Context, key *APIKey) (float64, error) {
	price := s.resolveOpenAIChannelPricing(ctx, "minimax-voice-clone", key)
	if price == nil || price.Mode != BillingModePerRequest || price.channelPricing == nil ||
		price.channelPricing.PerRequestPrice == nil || len(price.RequestTiers) != 0 {
		return 0, fmt.Errorf("configure minimax-voice-clone with a default per-request price (USD per successful clone)")
	}
	return *price.channelPricing.PerRequestPrice, nil
}
