package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type mediaKitASRTaskResponse struct {
	ID     string          `json:"id"`
	Model  string          `json:"model"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (h *OpenAIGatewayHandler) MediaKitASRTasks(c *gin.Context) {
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || key.Group == nil || key.Group.Platform != service.PlatformOpenAI {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "MediaKit ASR requires an OpenAI group")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), key.User, key, key.Group, subscription, service.QuotaPlatform(c.Request.Context(), key)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	if c.Request.Method == http.MethodPost {
		h.createMediaKitASRTask(c, key, subject)
		return
	}
	if c.Request.Method == http.MethodGet {
		h.getMediaKitASRTask(c, key, subject, subscription)
		return
	}
	c.Header("Allow", "GET, POST")
	h.errorResponse(c, http.StatusMethodNotAllowed, "invalid_request_error", "Method not allowed")
}

func (h *OpenAIGatewayHandler) createMediaKitASRTask(c *gin.Context, key *service.APIKey, subject middleware2.AuthSubject) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil || !gjson.ValidBytes(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model != service.MediaKitASRModel {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Unsupported MediaKit ASR model")
		return
	}
	if strings.TrimSpace(gjson.GetBytes(body, "audio_url").String()) == "" && strings.TrimSpace(gjson.GetBytes(body, "video_url").String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "audio_url or video_url is required")
		return
	}
	if h.gatewayService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "MediaKit ASR gateway is unavailable")
		return
	}
	ctx := c.Request.Context()
	reqLog := requestLogger(c, "handler.openai_gateway.mediakit_asr")
	selection, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
		ctx, key.GroupID, "", "", model, nil,
		service.OpenAIUpstreamTransportHTTPSSE,
		service.OpenAIEndpointCapabilityMediaKitASR,
		false, false, false, service.PlatformOpenAI,
	)
	if err != nil || selection == nil || selection.Account == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available MediaKit ASR account")
		return
	}
	account := selection.Account
	var started bool
	release, slotStatus := h.acquireResponsesAccountSlot(c, key.GroupID, "", selection, false, &started, reqLog)
	if slotStatus != openAISlotAcquireOK {
		return
	}
	defer release()
	if !account.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityMediaKitASR) {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Selected account is not configured for MediaKit ASR")
		return
	}
	upstreamBody, err := h.gatewayService.ForwardMediaKitASR(ctx, c, account, "", body)
	if err != nil {
		reqLog.Warn("mediakit_asr.submit_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		if !service.IsResponseCommitted(c) {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "MediaKit ASR task submission failed")
		}
		return
	}
	var upstream service.MediaKitASRForwardResponse
	if err := json.Unmarshal(upstreamBody, &upstream); err != nil || !upstream.Success || strings.TrimSpace(upstream.TaskID) == "" {
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", "MediaKit ASR returned an invalid task response")
		return
	}
	if err := h.gatewayService.BindMediaKitASRRequestAccount(ctx, key.GroupID, upstream.TaskID, subject.UserID, key.ID, account.ID); err != nil {
		reqLog.Error("mediakit_asr.bind_task_account_failed", zap.String("task_id", upstream.TaskID), zap.Error(err))
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Could not persist MediaKit ASR task routing")
		return
	}
	c.JSON(http.StatusOK, mediaKitASRTaskResponse{
		ID: upstream.TaskID, Model: model, Status: "queued",
	})
}

func (h *OpenAIGatewayHandler) getMediaKitASRTask(c *gin.Context, key *service.APIKey, subject middleware2.AuthSubject, subscription *service.UserSubscription) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" || strings.ContainsAny(taskID, "/?#\\") || taskID == "." || taskID == ".." {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Invalid task ID")
		return
	}
	ctx := c.Request.Context()
	accountID, err := h.gatewayService.ResolveMediaKitASRRequestAccount(ctx, key.GroupID, taskID, subject.UserID, key.ID)
	if err != nil || accountID <= 0 {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "MediaKit ASR task not found")
		return
	}
	sessionHash := service.MediaKitASRRequestSessionHash(taskID, subject.UserID, key.ID)
	selection, _, err := h.gatewayService.SelectMediaVideoRequestAccount(ctx, key.GroupID, sessionHash, accountID, service.MediaKitASRModel, service.PlatformOpenAI)
	if err != nil || selection == nil || selection.Account == nil || !selection.Account.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityMediaKitASR) {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "The MediaKit ASR account for this task is unavailable")
		return
	}
	account := selection.Account
	reqLog := requestLogger(c, "handler.openai_gateway.mediakit_asr")
	var started bool
	release, slotStatus := h.acquireResponsesAccountSlot(c, key.GroupID, sessionHash, selection, false, &started, reqLog)
	if slotStatus != openAISlotAcquireOK {
		return
	}
	defer release()
	queryStarted := time.Now()
	upstreamBody, err := h.gatewayService.ForwardMediaKitASR(ctx, c, account, taskID, nil)
	if err != nil {
		reqLog.Warn("mediakit_asr.query_failed", zap.Int64("account_id", account.ID), zap.String("task_id", taskID), zap.Error(err))
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", "MediaKit ASR task query failed")
		return
	}
	var upstream service.MediaKitASRForwardResponse
	if err := json.Unmarshal(upstreamBody, &upstream); err != nil {
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", "MediaKit ASR returned an invalid task response")
		return
	}
	status := service.MediaKitASRStatus(upstream)
	response := mediaKitASRTaskResponse{ID: taskID, Model: service.MediaKitASRModel, Status: status}
	if status == "succeeded" {
		response.Result = upstream.Result
		durationSeconds := service.MediaKitASRDurationSeconds(upstream.Result)
		if durationSeconds > 0 {
			result := &service.OpenAIForwardResult{
				RequestID:     "mediakit-asr:" + taskID,
				Model:         service.MediaKitASRModel,
				BillingModel:  service.MediaKitASRModel,
				UpstreamModel: service.MediaKitASRModel,
				Duration:      time.Since(queryStarted),
				AudioUsage:    &service.AudioUsage{Mode: "stt", DurationOrUnits: durationSeconds / 3600},
			}
			h.recordMediaKitASRUsage(c, key, account, subscription, taskID, result)
		}
	} else if status == "failed" {
		response.Error = upstream.Error
	}
	c.JSON(http.StatusOK, response)
}

func (h *OpenAIGatewayHandler) recordMediaKitASRUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	taskID string,
	result *service.OpenAIForwardResult,
) {
	if h == nil || c == nil || apiKey == nil || account == nil || result == nil || result.AudioUsage == nil {
		return
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload([]byte(taskID))
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	channelUsageFields := clientRequestedUsageFields(c, service.ChannelMappingResult{}, result.Model, result.UpstreamModel)
	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result: result, APIKey: apiKey, User: apiKey.User, Account: account, Subscription: subscription,
			InboundEndpoint: inboundEndpoint, UpstreamEndpoint: upstreamEndpoint, UserAgent: userAgent,
			IPAddress: clientIP, RequestPayloadHash: requestPayloadHash, APIKeyService: h.apiKeyService,
			QuotaPlatform: quotaPlatform, SessionID: sessionID,
			ChannelUsageFields: channelUsageFields,
		}); err != nil {
			logger.L().With(zap.String("component", "handler.openai_gateway.mediakit_asr"),
				zap.Int64("api_key_id", apiKey.ID), zap.Int64("account_id", account.ID),
				zap.String("task_id", taskID)).Error("mediakit_asr.record_usage_failed", zap.Error(err))
		}
	})
}
