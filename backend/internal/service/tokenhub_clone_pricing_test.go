package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTokenHubCloneIsOneIndependentPricedOperation(t *testing.T) {
	for _, tc := range []struct {
		name, operation, request, response, model string
		units                                     float64
		wantError                                 bool
	}{
		{"clone without preview", "clone", `{"model":"minimax-voice-clone","audio_url":"https://example.test/sample.mp3"}`, `{"request_id":"clone-1","voice_id":"minimax_new","usage":{"use_count":0},"base_resp":{"status_code":0}}`, "minimax-voice-clone", 1, false},
		{"clone preview keeps clone card", "clone", `{"model":"minimax-voice-clone","audio_url":"https://example.test/sample.mp3","tts_model":"minimax-speech-2.8-hd","text":"试听"}`, `{"request_id":"clone-2","voice_id":"minimax_new","usage":{"chars":47,"use_count":0},"base_resp":{"status_code":0}}`, "minimax-voice-clone", 1, false},
		{"TTS never adds a clone fee", "tts", `{"model":"minimax-speech-2.8-hd","text":"你好","voice_setting":{"voice_id":"minimax_new"}}`, `{"request_id":"tts-1","usage":{"chars":29,"total_tokens":991015,"use_count":1},"base_resp":{"status_code":0}}`, "minimax-speech-2.8-hd", 29.0 / 1e6, false},
		{"invalid clone is not billable", "clone", `{"model":"minimax-voice-clone","audio_url":"https://example.test/sample.mp3"}`, `{"base_resp":{"status_code":0}}`, "", 0, true},
		{"upstream failure is not billable", "clone", `{"model":"minimax-voice-clone","audio_url":"https://example.test/sample.mp3"}`, `{"base_resp":{"status_code":1001,"status_msg":"invalid audio"}}`, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.response))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tc.request))
			account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://tokenhub.tencentmaas.com", "api_key": "test-key", "openai_capabilities": []string{"tokenhub_speech"},
			}}
			_, result, err := svc.ForwardTokenHubMiniMaxSpeech(context.Background(), c, account, tc.operation, []byte(tc.request))
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.model, result.Model)
			require.Equal(t, tc.model, result.BillingModel)
			require.InDelta(t, tc.units, result.AudioUsage.DurationOrUnits, 1e-12)
			if tc.operation == "clone" {
				ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "another-http-request")
				require.Equal(t, result.RequestID, resolveUsageBillingRequestID(ctx, result.RequestID))
			}
		})
	}
}

func TestTokenHubCloneCardAndStandardSettlement(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	price := 1.4776
	group := &Group{ID: 6, Platform: PlatformComposite, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{
		{Models: []string{"minimax-voice-clone"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price},
	}}
	user := &User{ID: 1}
	key := &APIKey{ID: 3, GroupID: &group.ID, Group: group, User: user, Quota: 100}
	got, err := svc.TokenHubVoiceClonePrice(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, price, got)
	requestID := StableGrokAudioBillingRequestID("tokenhub-clone:real-response")
	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: requestID, Model: "minimax-voice-clone", BillingModel: "minimax-voice-clone", AudioUsage: &AudioUsage{Mode: "clone", DurationOrUnits: 1}},
		APIKey: key, User: user, Account: &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.calls)
	require.InDelta(t, price, billingRepo.lastCmd.BalanceCost, 1e-10)
	require.Equal(t, "minimax-voice-clone", usageRepo.lastLog.Model)
	require.Equal(t, requestID, usageRepo.lastLog.RequestID)
	group.ModelPricing = nil
	_, err = svc.TokenHubVoiceClonePrice(context.Background(), key)
	require.Error(t, err, "missing clone price must not fall back to TTS")
}
