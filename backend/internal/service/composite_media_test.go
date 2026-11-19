package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMediaVideoRequestPlatformChecksOwnerBeforeDispatch(t *testing.T) {
	for _, platform := range []string{PlatformHeyGen, PlatformGrok, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			groupID := int64(24)
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{{ID: 1, Platform: platform, GroupIDs: []int64{24}}}},
				cache:       &schedulerTestGatewayCache{},
			}
			ctx := context.Background()
			require.NoError(t, svc.BindGrokMediaVideoRequestAccount(ctx, &groupID, "task", 10, 20, 1))
			resolved, err := svc.ResolveMediaVideoRequestPlatform(ctx, &groupID, "task", 10, 20)
			if platform == PlatformOpenAI {
				require.Error(t, err)
				require.Empty(t, resolved)
			} else {
				require.NoError(t, err)
				require.Equal(t, platform, resolved)
			}
			for _, other := range []struct {
				group, user, key int64
				task             string
			}{
				{25, 10, 20, "task"}, {24, 11, 20, "task"}, {24, 10, 21, "task"}, {24, 10, 20, "other"},
			} {
				resolved, err := svc.ResolveMediaVideoRequestPlatform(ctx, &other.group, other.task, other.user, other.key)
				require.Error(t, err)
				require.Empty(t, resolved)
			}
		})
	}
}
