package service

import (
	"context"
	"slices"
)

// ResolveMediaVideoRequestPlatform only reveals a platform after matching the
// authenticated owner binding. The provider handler still enforces membership,
// account availability and fixed-account scheduling before querying upstream.
func (s *OpenAIGatewayService) ResolveMediaVideoRequestPlatform(ctx context.Context, groupID *int64, taskID string, userID, apiKeyID int64) (string, error) {
	accountID, err := s.ResolveGrokMediaVideoRequestAccount(ctx, groupID, taskID, userID, apiKeyID)
	if err != nil || accountID <= 0 || s.accountRepo == nil {
		return "", ErrNoAvailableAccounts
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil || account.ID != accountID || (groupID != nil && !slices.Contains(account.GroupIDs, *groupID)) {
		return "", ErrNoAvailableAccounts
	}
	switch account.Platform {
	case PlatformHeyGen, PlatformGrok:
		return account.Platform, nil
	default:
		return "", ErrNoAvailableAccounts
	}
}
