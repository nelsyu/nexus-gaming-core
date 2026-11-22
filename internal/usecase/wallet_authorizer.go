package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
)

type walletAuthorizer struct {
	users domain.UserRepository
}

// NewWalletAuthorizer 建立錢包的授權規則：
//
//	player            只能讀自己的錢包
//	admin / operator  只能讀取、調整同租戶使用者的錢包
//	其他角色          一律拒絕 (Deny by Default)
func NewWalletAuthorizer(users domain.UserRepository) domain.WalletAuthorizer {
	return &walletAuthorizer{users: users}
}

func (a *walletAuthorizer) AuthorizeRead(ctx context.Context, actor domain.Actor, targetUserID int64) error {
	switch actor.Role {
	case domain.RolePlayer:
		// 讀自己不需要查 DB，維持玩家查餘額的效能
		if actor.UserID == targetUserID {
			return nil
		}
	case domain.RoleAdmin, domain.RoleOperator:
		return a.requireSameTenant(ctx, actor, targetUserID)
	}
	return domain.ErrForbidden
}

func (a *walletAuthorizer) AuthorizeAdjust(ctx context.Context, actor domain.Actor, targetUserID int64) error {
	switch actor.Role {
	case domain.RoleAdmin, domain.RoleOperator:
		return a.requireSameTenant(ctx, actor, targetUserID)
	}
	return domain.ErrForbidden
}

func (a *walletAuthorizer) requireSameTenant(ctx context.Context, actor domain.Actor, targetUserID int64) error {
	if actor.TenantID == "" {
		return domain.ErrForbidden
	}
	target, err := a.users.GetByID(ctx, targetUserID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return domain.ErrWalletNotFound
	}
	if err != nil {
		return fmt.Errorf("get target user failed: %w", err) // 查不到租戶時不能放行
	}
	// 與「不存在」回同一個錯誤，不讓人探測其他租戶有哪些玩家
	if target.TenantID != actor.TenantID {
		return domain.ErrWalletNotFound
	}
	return nil
}
