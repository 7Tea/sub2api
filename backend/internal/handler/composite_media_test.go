//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdmitMediaPlatformPreservesCompositeGroupAndRejectsConflicts(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformHeyGen} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			c, _ := grokMediaSlotContext(context.Background(), method == http.MethodPost)
			key, _ := middleware.GetAPIKeyFromContext(c)
			group := key.Group
			group.Platform = service.PlatformComposite
			if method == http.MethodPost {
				require.False(t, admitMediaPlatform(c, key, platform), "unresolved create must not be admitted")
			}
			c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
			require.True(t, admitMediaPlatform(c, key, platform))
			require.Same(t, group, key.Group)
			require.Equal(t, service.PlatformComposite, key.Group.Platform)
			require.Equal(t, platform, service.QuotaPlatform(c.Request.Context(), key))
			c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), service.PlatformGemini))
			require.False(t, admitMediaPlatform(c, key, platform))
			key.Group.Platform = platform
			require.True(t, admitMediaPlatform(c, key, platform), "concrete group behavior is unchanged")
			key.Group.Platform = service.PlatformAnthropic
			require.False(t, admitMediaPlatform(c, key, platform))
		}
	}
}

type compositeMediaBindings struct {
	grokMediaSlotBindings
	owners map[string]int64
}

func (s *compositeMediaBindings) GetSessionAccountID(_ context.Context, group int64, key string) (int64, error) {
	if id := s.owners[fmt.Sprintf("%d:%s", group, key)]; id > 0 {
		return id, nil
	}
	return 0, service.ErrStickySessionNotFound
}

func (s *compositeMediaBindings) SetSessionAccountID(_ context.Context, group int64, key string, owner int64, _ time.Duration) error {
	s.owners[fmt.Sprintf("%d:%s", group, key)] = owner
	return nil
}

type compositeMediaUsageRepo struct {
	service.UsageLogRepository
	logs map[string]*service.UsageLog
}

func (r *compositeMediaUsageRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	key := fmt.Sprintf("%d:%s", log.APIKeyID, log.RequestID)
	if r.logs[key] != nil {
		return false, nil
	}
	r.logs[key] = log
	return true, nil
}

func TestCompositeMediaSingleKeyLifecycleAndOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := []service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Credentials: map[string]any{"base_url": "https://tokenhub.tencentmaas.com", "api_key": "speech-key", "openai_capabilities": []string{"tokenhub_speech"}}},
		{ID: 2, Platform: service.PlatformOpenAI, Credentials: map[string]any{"base_url": "https://mediakit.cn-beijing.volces.com/api/v1", "api_key": "asr-key", "openai_capabilities": []string{"mediakit_asr"}}},
		{ID: 3, Platform: service.PlatformHeyGen, Credentials: map[string]any{"base_url": "https://api.heygen.com/v3", "api_key": "video-key"}},
	}
	for i := range accounts {
		accounts[i].Type = service.AccountTypeAPIKey
		accounts[i].Status = service.StatusActive
		accounts[i].Schedulable = true
		accounts[i].Concurrency = 10
		accounts[i].GroupIDs = []int64{24}
	}
	repo := openAIImagesFailoverAccountRepo{accounts: accounts}
	slots := &grokMediaSlotsCache{accounts: map[string]int64{}, users: map[string]int64{}}
	concurrency := service.NewConcurrencyService(slots)
	bindings := &compositeMediaBindings{owners: map[string]int64{}}
	usage := &compositeMediaUsageRepo{logs: map[string]*service.UsageLog{}}
	upstream := &grokMediaSlotUpstream{call: func(req *http.Request, id int64) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v1/wand/minimax-tts/sync_clone":
			require.Equal(t, int64(1), id)
			body = `{"voice_id":"test-cloned-voice","base_resp":{"status_code":0}}`
		case "/v1/wand/minimax-tts/sync_tts":
			require.Equal(t, int64(1), id)
			body = `{"request_id":"tts-test","usage":{"chars":26},"data":{"audio":"https://example.test/tts.mp3"},"base_resp":{"status_code":0}}`
		case "/api/v1/tools/asr-subtitles":
			require.Equal(t, int64(2), id)
			body = `{"success":true,"task_id":"asr-test","status":"queued"}`
		case "/api/v1/tasks/asr-test":
			require.Equal(t, int64(2), id)
			body = `{"success":true,"status":"completed","result":{"duration_seconds":68.7,"subtitles":[{"subtitle_text":"测试字幕"}]}}`
		case "/v3/videos":
			require.Equal(t, int64(3), id)
			body = `{"data":{"video_id":"heygen-test","status":"waiting"}}`
		case "/v3/videos/heygen-test":
			require.Equal(t, int64(3), id)
			body = `{"data":{"status":"completed","duration":68.7281,"video_url":"https://example.test/result.mp4"}}`
		default:
			t.Fatalf("unexpected upstream URL %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	pricing := &service.BillingService{}
	gateway := service.NewOpenAIGatewayService(repo, usage, nil, nil, nil, nil, bindings, cfg, nil, concurrency, pricing, nil, nil, upstream, service.NewDeferredService(repo, nil, time.Hour), nil, nil, service.NewModelPricingResolver(nil, pricing), nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, nil, nil, nil, nil, nil, cfg)
	requestSequence := 0
	newContext := func(method, taskID, body, platform string) (*gin.Context, *httptest.ResponseRecorder) {
		c, w := grokMediaSlotContext(context.Background(), method == http.MethodPost)
		c.Request = httptest.NewRequest(method, "/test", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		requestSequence++
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, fmt.Sprintf("http-request-%d", requestSequence)))
		if platform != "" {
			c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
		}
		c.Params = gin.Params{{Key: "task_id", Value: taskID}, {Key: "request_id", Value: taskID}}
		key, _ := middleware.GetAPIKeyFromContext(c)
		key.Group.Platform = service.PlatformComposite
		key.Group.RateMultiplier = 1
		tts, asr, video, clone := 2.0, 1.0, 0.05, 1.4776
		key.Group.ModelPricing = []service.ChannelModelPricing{
			{Models: []string{"minimax-speech-2.8-hd"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &tts},
			{Models: []string{service.MediaKitASRModel}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &asr},
			{Models: []string{service.HeyGenVideoModel}, BillingMode: service.BillingModeVideo, PerRequestPrice: &video},
			{Models: []string{"minimax-voice-clone"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &clone},
		}
		return c, w
	}
	for _, call := range []struct {
		body string
		fn   func(*gin.Context)
	}{
		{`{"model":"minimax-voice-clone","audio_url":"https://example.test/voice.mp3"}`, h.TokenHubMiniMaxVoiceClone},
		{`{"model":"minimax-speech-2.8-hd","text":"你好","voice_setting":{"voice_id":"test-cloned-voice"}}`, h.TokenHubMiniMaxTTS},
		{`{"model":"volcengine-asr-subtitles","audio_url":"https://example.test/voice.mp3"}`, h.MediaKitASRTasks},
		{`{"model":"heygen-avatar-video","type":"image","image":{"type":"url","url":"https://example.test/person.jpg"},"audio_url":"https://example.test/voice.mp3","resolution":"720p"}`, h.HeyGenVideos},
	} {
		platform := service.PlatformOpenAI
		if strings.Contains(call.body, service.HeyGenVideoModel) {
			platform = service.PlatformHeyGen
		}
		c, w := newContext(http.MethodPost, "", call.body, platform)
		call.fn(c)
		require.Equal(t, 200, w.Code, w.Body.String())
		slots.assertReleased(t)
	}
	for range 3 {
		c, w := newContext(http.MethodGet, "asr-test", "", "")
		h.MediaKitASRTasks(c)
		require.Equal(t, 200, w.Code, w.Body.String())
		c, w = newContext(http.MethodGet, "heygen-test", "", "")
		h.CompositeVideoStatus(c)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "result.mp4")
		slots.assertReleased(t)
	}
	require.Len(t, usage.logs, 4, "one clone charge is separate from TTS; repeated ASR/video polls deduplicate")
	for _, log := range usage.logs {
		require.Equal(t, int64(20), log.APIKeyID)
		require.Equal(t, int64(24), *log.GroupID)
		switch log.Model {
		case "minimax-voice-clone":
			require.InDelta(t, 1.4776, log.TotalCost, 1e-9)
		case service.HeyGenVideoModel:
			require.Equal(t, 69, *log.VideoDurationSeconds)
			require.InDelta(t, 3.45, log.TotalCost, 1e-9)
		case service.MediaKitASRModel:
			require.Equal(t, "grok_audio:mediakit-asr:asr-test", log.RequestID)
			require.InDelta(t, 68.7/3600, log.TotalCost, 1e-9)
		default:
			require.InDelta(t, 26*2.0/1e6, log.TotalCost, 1e-9)
		}
	}
	for _, task := range []string{"heygen-test", "asr-test"} {
		for _, changed := range []string{"key", "user", "group", "task"} {
			c, w := newContext(http.MethodGet, task, "", "")
			key, _ := middleware.GetAPIKeyFromContext(c)
			switch changed {
			case "key":
				key.ID = 21
			case "user":
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11})
			case "group":
				other := int64(25)
				key.GroupID = &other
			case "task":
				c.Params = gin.Params{{Key: "request_id", Value: "unknown"}, {Key: "task_id", Value: "unknown"}}
			}
			before := upstream.calls
			if task == "heygen-test" {
				h.CompositeVideoStatus(c)
			} else {
				h.MediaKitASRTasks(c)
			}
			require.Equal(t, 404, w.Code, w.Body.String())
			require.Equal(t, before, upstream.calls)
		}
	}
}
