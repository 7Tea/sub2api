package handler

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) TokenHubMiniMaxTTS(c *gin.Context) {
	h.tokenHubMiniMaxSpeech(c, "tts")
}

func (h *OpenAIGatewayHandler) TokenHubMiniMaxVoiceClone(c *gin.Context) {
	h.tokenHubMiniMaxSpeech(c, "clone")
}

func (h *OpenAIGatewayHandler) tokenHubMiniMaxSpeech(c *gin.Context, operation string) {
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || !admitMediaPlatform(c, key, service.PlatformOpenAI) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "TokenHub MiniMax speech requires an OpenAI group or a composite route to OpenAI")
		return
	}
	if _, ok := middleware2.GetAuthSubjectFromContext(c); !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if c.Request.Method != http.MethodPost {
		c.Header("Allow", http.MethodPost)
		h.errorResponse(c, http.StatusMethodNotAllowed, "invalid_request_error", "Method not allowed")
		return
	}
	if c.GetHeader("Content-Type") != "" && !strings.Contains(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		h.errorResponse(c, http.StatusUnsupportedMediaType, "invalid_request_error", "TokenHub MiniMax speech requires application/json")
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil || !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a JSON object")
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	selectionModel := model
	if operation == "clone" {
		if model != "minimax-voice-clone" {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model must be minimax-voice-clone")
			return
		}
		selectionModel = strings.TrimSpace(gjson.GetBytes(body, "tts_model").String())
	}
	if h.gatewayService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "TokenHub speech gateway is unavailable")
		return
	}
	if operation == "tts" && strings.TrimSpace(selectionModel) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if operation == "tts" && strings.TrimSpace(gjson.GetBytes(body, "text").String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "text is required")
		return
	}
	if operation == "clone" && strings.TrimSpace(gjson.GetBytes(body, "audio_url").String()) == "" &&
		strings.TrimSpace(gjson.GetBytes(body, "audio_data").String()) == "" && !gjson.GetBytes(body, "file_id").Exists() {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "audio_url, audio_data, or file_id is required")
		return
	}

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if !ok || apiKey == nil {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}
	if operation == "tts" || strings.TrimSpace(gjson.GetBytes(body, "text").String()) != "" {
		estimate := grokVoiceInflightEstimate("tts", body)
		inflightDone, reserveErr := reserveInflightBalance(c, h.billingCacheService, h.gatewayService, apiKey, subscription, estimate)
		if reserveErr != nil {
			status, code, message, retryAfter := billingErrorDetails(reserveErr)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.errorResponse(c, status, code, message)
			return
		}
		defer inflightDone()
	}

	ctx := c.Request.Context()
	reqLog := requestLogger(c, "handler.openai_gateway.tokenhub_speech", zap.String("operation", operation))
	selection, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
		ctx, apiKey.GroupID, "", "", selectionModel, nil,
		service.OpenAIUpstreamTransportHTTPSSE,
		service.OpenAIEndpointCapabilityTokenHubSpeech,
		false, false, false, service.PlatformOpenAI,
	)
	if err != nil || selection == nil || selection.Account == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available TokenHub MiniMax speech account")
		return
	}
	account := selection.Account
	var started bool
	release, slotStatus := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &started, reqLog)
	if slotStatus != openAISlotAcquireOK {
		return
	}
	defer release()
	responseBody, result, err := h.gatewayService.ForwardTokenHubMiniMaxSpeech(ctx, c, account, operation, body)
	if err != nil {
		reqLog.Warn("tokenhub_speech.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", responseBody)
	if result != nil && result.AudioUsage != nil {
		h.recordTokenHubSpeechUsage(c, apiKey, account, subscription, operation, result)
	}
}

func (h *OpenAIGatewayHandler) recordTokenHubSpeechUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	operation string,
	result *service.OpenAIForwardResult,
) {
	if h == nil || c == nil || apiKey == nil || account == nil || result == nil || result.AudioUsage == nil {
		return
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload([]byte(result.RequestID))
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	channelUsageFields := clientRequestedUsageFields(c, service.ChannelMappingResult{}, result.Model, result.UpstreamModel)
	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result: result, APIKey: apiKey, User: apiKey.User, Account: account, Subscription: subscription,
			InboundEndpoint: inboundEndpoint, UpstreamEndpoint: upstreamEndpoint, UserAgent: userAgent,
			IPAddress: clientIP, RequestPayloadHash: requestPayloadHash, APIKeyService: h.apiKeyService,
			QuotaPlatform: quotaPlatform, SessionID: sessionID, ChannelUsageFields: channelUsageFields,
		}); err != nil {
			logger.L().With(zap.String("component", "handler.openai_gateway.tokenhub_speech"),
				zap.Int64("api_key_id", apiKey.ID), zap.Int64("account_id", account.ID),
				zap.String("operation", operation)).Error("tokenhub_speech.record_usage_failed", zap.Error(err))
		}
	})
}
