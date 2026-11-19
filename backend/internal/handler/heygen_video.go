package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// HeyGenVideos exposes HeyGen's asynchronous image-plus-audio video workflow
// through a dedicated HeyGen group and its native v3 request/response shapes.
func (h *OpenAIGatewayHandler) HeyGenVideos(c *gin.Context) {
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || !admitMediaPlatform(c, key, service.PlatformHeyGen) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "HeyGen videos require a HeyGen group or a composite route to HeyGen")
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
	if h.gatewayService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "HeyGen gateway is unavailable")
		return
	}
	if c.Request.Method == http.MethodPost {
		h.createHeyGenVideo(c, key, subject, subscription)
		return
	}
	if c.Request.Method == http.MethodGet {
		h.getHeyGenVideo(c, key, subject, subscription)
		return
	}
	c.Header("Allow", "GET, POST")
	h.errorResponse(c, http.StatusMethodNotAllowed, "invalid_request_error", "Method not allowed")
}

func (h *OpenAIGatewayHandler) HeyGenModels(c *gin.Context) {
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || key.Group == nil || key.Group.Platform != service.PlatformHeyGen {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "HeyGen models require a HeyGen group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": []gin.H{{"id": service.HeyGenVideoModel, "object": "model", "created": 0, "owned_by": "heygen"}}})
}

func (h *OpenAIGatewayHandler) createHeyGenVideo(c *gin.Context, key *service.APIKey, subject middleware2.AuthSubject, subscription *service.UserSubscription) {
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a valid JSON object")
		return
	}
	if len(body) == 0 || !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a valid JSON object")
		return
	}
	if strings.TrimSpace(gjson.GetBytes(body, "model").String()) != service.HeyGenVideoModel {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model must be heygen-avatar-video")
		return
	}
	resolution := strings.TrimSpace(gjson.GetBytes(body, "resolution").String())
	if resolution == "" {
		resolution = service.VideoBillingResolution720P
	}
	if resolution != service.VideoBillingResolution720P && resolution != service.VideoBillingResolution1080P {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "HeyGen resolution must be 720p or 1080p")
		return
	}
	if key.Group.GetVideoPriceForModel(service.HeyGenVideoModel, resolution) == nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "HeyGen video price is not configured for this resolution")
		return
	}
	if !h.selectHeyGenVideoAccount(c, key, service.HeyGenVideoModel, "", subject, subscription, body) {
		return
	}
}

func (h *OpenAIGatewayHandler) selectHeyGenVideoAccount(c *gin.Context, key *service.APIKey, model, taskID string, subject middleware2.AuthSubject, subscription *service.UserSubscription, body []byte) bool {
	ctx := c.Request.Context()
	reqLog := requestLogger(c, "handler.openai_gateway.heygen_video", zap.String("task_id", taskID))
	var selection *service.AccountSelectionResult
	var err error
	if taskID == "" {
		selection, _, err = h.gatewayService.SelectAccountWithSchedulerForCapability(
			ctx, key.GroupID, "", "", model, nil, service.OpenAIUpstreamTransportHTTPSSE,
			"", false, false, false, service.PlatformHeyGen,
		)
	} else {
		accountID, resolveErr := h.gatewayService.ResolveGrokMediaVideoRequestAccount(ctx, key.GroupID, taskID, subject.UserID, key.ID)
		if resolveErr != nil || accountID <= 0 {
			h.errorResponse(c, http.StatusNotFound, "not_found_error", "HeyGen video task not found")
			return false
		}
		sessionHash := service.GrokMediaVideoRequestSessionHash(taskID, subject.UserID, key.ID)
		selection, _, err = h.gatewayService.SelectMediaVideoRequestAccount(ctx, key.GroupID, sessionHash, accountID, model, service.PlatformHeyGen)
	}
	if err != nil || selection == nil || selection.Account == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available HeyGen account for this task")
		return false
	}
	account := selection.Account
	var started bool
	release, slotStatus := h.acquireResponsesAccountSlot(c, key.GroupID, "", selection, false, &started, reqLog)
	if slotStatus != openAISlotAcquireOK {
		return false
	}
	defer release()
	upstreamBody, result, err := h.gatewayService.ForwardHeyGenVideo(ctx, c, account, taskID, body)
	if err != nil {
		reqLog.Warn("heygen_video.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", "HeyGen video request failed")
		return false
	}
	if taskID == "" {
		if err := h.gatewayService.BindGrokMediaVideoRequestAccount(ctx, key.GroupID, result.ResponseID, subject.UserID, key.ID, account.ID); err != nil {
			reqLog.Error("heygen_video.bind_task_account_failed", zap.String("task_id", result.ResponseID), zap.Error(err))
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Could not persist HeyGen task routing")
			return false
		}
		resolution := strings.TrimSpace(gjson.GetBytes(body, "resolution").String())
		if resolution == "" {
			resolution = service.VideoBillingResolution720P
		}
		pending := service.GrokVideoPendingBilling{Model: service.HeyGenVideoModel, BillingModel: service.HeyGenVideoModel, UpstreamModel: service.HeyGenVideoModel, VideoResolution: resolution, CreatedAt: service.GrokVideoPendingCreatedAtNow()}
		if err := h.gatewayService.StoreGrokVideoPendingBilling(ctx, result.ResponseID, subject.UserID, key.ID, pending); err != nil {
			reqLog.Error("heygen_video.store_pending_billing_failed", zap.String("task_id", result.ResponseID), zap.Error(err))
		}
		c.Data(http.StatusOK, "application/json", upstreamBody)
		return true
	}
	status, durationSeconds, resolution := service.HeyGenVideoStatus(upstreamBody)
	if status == "completed" && durationSeconds > 0 && strings.TrimSpace(gjson.GetBytes(upstreamBody, "data.video_url").String()) != "" {
		pending, loadErr := h.gatewayService.LoadGrokVideoPendingBilling(ctx, taskID, subject.UserID, key.ID)
		if loadErr == nil && pending != nil {
			claimed, claimErr := h.gatewayService.ClaimGrokVideoBilling(ctx, taskID, subject.UserID, key.ID)
			if claimErr == nil && claimed {
				if resolution == "" {
					resolution = pending.VideoResolution
				}
				result.Model = service.HeyGenVideoModel
				result.BillingModel = service.HeyGenVideoModel
				result.UpstreamModel = service.HeyGenVideoModel
				result.ResponseID = taskID
				result.RequestID = service.StableGrokVideoBillingRequestID(taskID)
				result.VideoCount = 1
				result.VideoDurationSeconds = durationSeconds
				result.VideoResolution = resolution
				result.Duration = time.Since(parseHeyGenCreatedAt(pending.CreatedAt))
				recordGrokMediaUsage(c, h, reqLog, key, subject, subscription, account, result, service.HeyGenVideoModel, nil, taskID)
			}
		}
	}
	c.Data(http.StatusOK, "application/json", upstreamBody)
	return true
}

func parseHeyGenCreatedAt(value string) time.Time {
	createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Now()
	}
	return createdAt
}

func (h *OpenAIGatewayHandler) getHeyGenVideo(c *gin.Context, key *service.APIKey, subject middleware2.AuthSubject, subscription *service.UserSubscription) {
	taskID := strings.TrimSpace(c.Param("request_id"))
	if taskID == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "video_id is required")
		return
	}
	h.selectHeyGenVideoAccount(c, key, service.HeyGenVideoModel, taskID, subject, subscription, nil)
}
