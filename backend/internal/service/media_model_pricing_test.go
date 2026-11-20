package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMediaModelPricingUsesNativeUnitsInOneCompositeGroup(t *testing.T) {
	billing := &BillingService{}
	svc := &OpenAIGatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
	price := func(value float64) *float64 { return &value }
	group := &Group{ID: 24, Platform: PlatformComposite, ModelPricing: []ChannelModelPricing{
		{Models: []string{HeyGenVideoModel}, BillingMode: BillingModeVideo, PerRequestPrice: price(0.15), Intervals: []PricingInterval{
			{TierLabel: "1080p", PerRequestPrice: price(0.20)},
		}},
		{Models: []string{"minimax-speech-2.8-hd"}, BillingMode: BillingModePerRequest, PerRequestPrice: price(1.5)},
		{Models: []string{"minimax-speech-2.8-turbo"}, BillingMode: BillingModePerRequest, PerRequestPrice: price(0.75)},
		{Models: []string{MediaKitASRModel}, BillingMode: BillingModePerRequest, PerRequestPrice: price(0.6)},
	}}
	// Existing broad group rates must not override the selected model's card.
	group.VideoPrice720P, group.AudioTTSPricePerMillionChars, group.AudioSTTPricePerHour = price(99), price(99), price(99)
	key := &APIKey{GroupID: &group.ID, Group: group}
	for _, tc := range []struct {
		name, model string
		result      OpenAIForwardResult
		want        float64
	}{
		{"heygen full duration", HeyGenVideoModel, OpenAIForwardResult{VideoCount: 1, VideoResolution: "720p", VideoDurationSeconds: 69}, 10.35},
		{"heygen resolution tier", HeyGenVideoModel, OpenAIForwardResult{VideoCount: 1, VideoResolution: "1080p", VideoDurationSeconds: 69}, 13.8},
		{"tts hd characters", "minimax-speech-2.8-hd", OpenAIForwardResult{AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 1000.0 / 1e6}}, 0.0015},
		{"tts turbo own price", "minimax-speech-2.8-turbo", OpenAIForwardResult{AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 1000.0 / 1e6}}, 0.00075},
		{"asr audio duration", MediaKitASRModel, OpenAIForwardResult{AudioUsage: &AudioUsage{Mode: "stt", DurationOrUnits: 90.0 / 3600}}, 0.015},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.result.Model = tc.model
			cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), &tc.result, key, []string{tc.model},
				2, 2, 2, 2, UsageTokens{}, "", nil, false, time.Time{})
			require.NoError(t, err)
			require.InDelta(t, tc.want, cost.TotalCost, 1e-12)
			require.InDelta(t, tc.want*2, cost.ActualCost, 1e-12)
			require.Zero(t, tc.result.Usage.OutputTokens, "media units must not become synthetic output tokens")
		})
	}
}

func TestHeyGenVideoPricingAdmissionMatchesModelCards(t *testing.T) {
	price := func(value float64) *float64 { return &value }
	for _, tc := range []struct {
		name       string
		card       *ChannelModelPricing
		legacy     *float64
		resolution string
		want       bool
	}{
		{name: "missing price", resolution: "720p"},
		{name: "legacy price retained", legacy: price(0.15), resolution: "720p", want: true},
		{name: "default model price", card: &ChannelModelPricing{BillingMode: BillingModeVideo, PerRequestPrice: price(0.15)}, resolution: "720p", want: true},
		{name: "explicit free default", card: &ChannelModelPricing{BillingMode: BillingModeVideo, PerRequestPrice: price(0)}, resolution: "720p", want: true},
		{name: "matching resolution", card: &ChannelModelPricing{BillingMode: BillingModeVideo, Intervals: []PricingInterval{{TierLabel: "1080p", PerRequestPrice: price(0.2)}}}, resolution: "1080p", want: true},
		{name: "other resolution is not a price", card: &ChannelModelPricing{BillingMode: BillingModeVideo, Intervals: []PricingInterval{{TierLabel: "1080p", PerRequestPrice: price(0.2)}}}, resolution: "720p"},
		{name: "empty model card overrides legacy", card: &ChannelModelPricing{BillingMode: BillingModeVideo}, legacy: price(0.15), resolution: "720p"},
		{name: "token card cannot bill duration", card: &ChannelModelPricing{BillingMode: BillingModeToken, OutputPrice: price(1e-6)}, legacy: price(0.15), resolution: "720p"},
		{name: "per request is not per second", card: &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: price(0.15)}, resolution: "720p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			billing := &BillingService{}
			svc := &OpenAIGatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
			group := &Group{ID: 24, Platform: PlatformComposite, VideoPrice720P: tc.legacy}
			if tc.card != nil {
				tc.card.Models = []string{HeyGenVideoModel}
				group.ModelPricing = []ChannelModelPricing{*tc.card}
			}
			require.Equal(t, tc.want, svc.HasHeyGenVideoPricing(context.Background(), &APIKey{Group: group}, tc.resolution))
		})
	}
}
