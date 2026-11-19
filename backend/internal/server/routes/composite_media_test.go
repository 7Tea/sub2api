package routes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompositeMediaModelRoutingPreservesGroupAndBody(t *testing.T) {
	for _, tc := range []struct{ path, model, target string }{
		{"/v1/tts", "minimax-speech-2.8-hd", service.PlatformOpenAI},
		{"/v1/custom-voices", "minimax-voice-clone", service.PlatformOpenAI},
		{"/v1/audio/transcriptions/tasks", service.MediaKitASRModel, service.PlatformOpenAI},
		{"/v1/videos", service.HeyGenVideoModel, service.PlatformHeyGen},
	} {
		t.Run(tc.path, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			key := &service.APIKey{Group: &service.Group{ID: 24, Platform: service.PlatformComposite}}
			router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), key); c.Next() })
			router.Use(compositeTargetPlatformMiddleware(nil))
			body := `{"model":"` + tc.model + `"}`
			router.POST(tc.path, func(c *gin.Context) {
				require.Equal(t, tc.target, getGroupPlatform(c))
				require.Equal(t, service.PlatformComposite, key.Group.Platform)
				require.Equal(t, tc.target, service.QuotaPlatform(c.Request.Context(), key))
				got, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.JSONEq(t, body, string(got))
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		})
	}
}

func TestCompositeMediaRegisteredRoutesReachTheirHandlers(t *testing.T) {
	for _, prefix := range []string{"/v1", ""} {
		for _, tc := range []struct{ path, model string }{
			{"/tts", "minimax-speech-2.8-hd"},
			{"/custom-voices", "minimax-voice-clone"},
			{"/videos", service.HeyGenVideoModel},
		} {
			router := newGatewayRoutesTestRouter(service.PlatformComposite)
			req := httptest.NewRequest(http.MethodPost, prefix+tc.path, strings.NewReader(`{"model":"`+tc.model+`"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			// The fixture intentionally has no user subject: a 401 shows dispatch
			// reached the intended handler past the old platform/group rejection.
			require.Equal(t, http.StatusUnauthorized, w.Code, "%s: %s", prefix+tc.path, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/audio/transcriptions/tasks", "/v1/videos/task", "/videos/task", "/v1/audio/transcriptions/tasks/task"} {
		router := newGatewayRoutesTestRouter(service.PlatformComposite)
		method := http.MethodGet
		if path == "/v1/audio/transcriptions/tasks" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, path, strings.NewReader(`{"model":"volcengine-asr-subtitles"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s: %s", path, w.Body.String())
	}
}
