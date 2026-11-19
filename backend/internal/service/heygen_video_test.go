package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeyGenVideoStatusReadsCompletedVideoBillingFields(t *testing.T) {
	status, duration, resolution := HeyGenVideoStatus(json.RawMessage(`{"data":{"status":"completed","duration":12.2,"video_url":"https://example.test/video.mp4"}}`))
	require.Equal(t, "completed", status)
	require.Equal(t, 13, duration)
	require.Empty(t, resolution)
}

func TestHeyGenBaseURLUsesHeyGenCredentials(t *testing.T) {
	account := &Account{Platform: PlatformHeyGen, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.heygen.com/v3/"}}
	require.Empty(t, account.GetOpenAIBaseURL())
	base, err := heyGenBaseURL(account)
	require.NoError(t, err)
	require.Equal(t, "https://api.heygen.com/v3", base)
	for _, invalid := range []string{"", "http://api.heygen.com/v3", "https://example.com/v3", "https://api.heygen.com/v3?x=1", "https://api.heygen.com:9999/v3"} {
		account.Credentials["base_url"] = invalid
		_, err := heyGenBaseURL(account)
		require.Error(t, err, invalid)
	}
}

func TestHeyGenVideoStatusKeepsTasksUnbillableUntilCompleted(t *testing.T) {
	for _, upstreamStatus := range []string{"pending", "processing", "failed"} {
		status, duration, _ := HeyGenVideoStatus(json.RawMessage(`{"data":{"status":"` + upstreamStatus + `","duration":9}}`))
		if upstreamStatus == "failed" {
			require.Equal(t, "failed", status)
		} else {
			require.Equal(t, "processing", status)
		}
		require.Equal(t, 9, duration)
	}
}

func TestHeyGenVideoCostUsesFullCompletedDuration(t *testing.T) {
	price := 0.01
	prices := &VideoPriceConfig{Price720P: &price}
	billing := &BillingService{}
	status, seconds, _ := HeyGenVideoStatus([]byte(`{"data":{"status":"completed","duration":68.7281}}`))
	require.Equal(t, "completed", status)
	require.Equal(t, 69, seconds)
	cost := billing.CalculateVideoCost(HeyGenVideoModel, "720p", 1, seconds, prices, 1)
	require.InDelta(t, 0.69, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.69, cost.ActualCost, 1e-12)

	// The fix must preserve Grok's default duration and request cap.
	require.InDelta(t, 0.15, billing.CalculateVideoCost("grok-imagine-video", "720p", 1, seconds, prices, 1).ActualCost, 1e-12)
	require.InDelta(t, 0.08, billing.CalculateVideoCost("grok-imagine-video", "720p", 1, 0, prices, 1).ActualCost, 1e-12)
	// Missing HeyGen duration must never turn into Grok's default 8 seconds.
	for _, missing := range []int{0, -1} {
		require.Zero(t, billing.CalculateVideoCost(HeyGenVideoModel, "720p", 1, missing, prices, 1).ActualCost)
	}
	// Longer videos, multiple outputs and the existing multiplier remain linear.
	require.InDelta(t, 12.0, billing.CalculateVideoCost(HeyGenVideoModel, "720p", 2, 300, prices, 2).ActualCost, 1e-12)
}
