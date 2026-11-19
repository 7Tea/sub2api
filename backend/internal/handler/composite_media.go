package handler

import (
	"net/http"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// admitMediaPlatform keeps the authenticated group intact for pricing and
// ownership. Only the concrete platform on the request context is resolved.
func admitMediaPlatform(c *gin.Context, key *service.APIKey, platform string) bool {
	if key == nil || key.Group == nil {
		return false
	}
	if key.Group.Platform != service.PlatformComposite {
		return key.Group.Platform == platform
	}
	resolved, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	if ok && resolved != platform {
		return false
	}
	// Create requests must have passed the model resolver. ASR task GETs use
	// their dedicated endpoint; video GETs use CompositeVideoStatus below.
	if !ok && c.Request.Method != http.MethodGet {
		return false
	}
	c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
	return true
}

// CompositeVideoStatus dispatches a model-less lookup using its owner binding,
// rather than guessing Grok from the shared /videos/:request_id URL.
func (h *OpenAIGatewayHandler) CompositeVideoStatus(c *gin.Context) {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key == nil || key.Group == nil || key.Group.Platform != service.PlatformComposite {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "A composite group is required")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if h.gatewayService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video gateway is unavailable")
		return
	}
	platform, err := h.gatewayService.ResolveMediaVideoRequestPlatform(c.Request.Context(), key.GroupID, c.Param("request_id"), subject.UserID, key.ID)
	if err != nil {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Video request not found")
		return
	}
	c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
	switch platform {
	case service.PlatformHeyGen:
		h.HeyGenVideos(c)
	case service.PlatformGrok:
		h.GrokVideoStatus(c)
	default:
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Video request not found")
	}
}
