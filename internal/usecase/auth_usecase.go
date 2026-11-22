package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/shopspring/decimal"
)

type authUsecase struct {
	uow              domain.UnitOfWork
	userRepo         domain.UserRepository
	walletRepo       domain.WalletRepository
	hasher           domain.PasswordHasher
	tokenManager     domain.TokenManager
	refreshTokenRepo domain.RefreshTokenRepository
	blacklist        domain.TokenBlacklist
	refreshTTL       time.Duration
}

func NewAuthUsecase(
	uow domain.UnitOfWork,
	userRepo domain.UserRepository,
	walletRepo domain.WalletRepository,
	hasher domain.PasswordHasher,
	tokenManager domain.TokenManager,
	refreshTokenRepo domain.RefreshTokenRepository,
	blacklist domain.TokenBlacklist,
) domain.AuthUsecase {
	return &authUsecase{
		uow:              uow,
		userRepo:         userRepo,
		walletRepo:       walletRepo,
		hasher:           hasher,
		tokenManager:     tokenManager,
		refreshTokenRepo: refreshTokenRepo,
		blacklist:        blacklist,
		refreshTTL:       7 * 24 * time.Hour,
	}
}

// Register 公開註冊：一律建立玩家，並在同一個交易 (UoW) 中建立錢包
func (u *authUsecase) Register(ctx context.Context, req *domain.RegisterRequest) (*domain.User, error) {
	// 1. 雜湊密碼 (不可逆 + Salt)
	hashedPassword, err := u.hasher.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	user := &domain.User{
		TenantID:     req.TenantID,
		Username:     req.Username,
		PasswordHash: hashedPassword,
		Role:         domain.RolePlayer,
		Currency:     req.Currency,
	}

	// 2. 透過 UnitOfWork 保證 User 建立與預設 Wallet 建立的原子性
	err = u.uow.DoTx(ctx, func(txCtx context.Context) error {
		if err := u.userRepo.CreateUser(txCtx, user); err != nil {
			return err
		}

		wallet := &domain.Wallet{
			UserID:   user.ID,
			Balance:  decimal.Zero,
			Currency: user.Currency,
			Version:  1,
		}
		if err := u.walletRepo.CreateWallet(txCtx, wallet); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return user, nil
}

// Login 驗證身分並簽發 Access Token 與 Opaque Refresh Token (存入 Redis)
func (u *authUsecase) Login(ctx context.Context, req *domain.LoginRequest) (*domain.TokenPair, error) {
	user, err := u.userRepo.GetByUsername(ctx, req.TenantID, req.Username)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			// 資安最佳實踐：防止帳號枚舉攻擊 (User Enumeration)，統一回覆憑證錯誤
			return nil, domain.ErrInvalidCredentials
		}
		return nil, err
	}

	if err := u.hasher.ComparePassword(user.PasswordHash, req.Password); err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	tokenPair, err := u.tokenManager.GenerateTokenPair(user)
	if err != nil {
		return nil, err
	}

	// 將 Opaque Refresh Token 寫入快取 (保存 SHA-256 雜湊與 TTL)
	if err := u.refreshTokenRepo.StoreRefreshToken(ctx, tokenPair.RefreshToken, user.ID, u.refreshTTL); err != nil {
		return nil, err
	}

	return tokenPair, nil
}

// RefreshToken 使用 Opaque Refresh Token 續期，並實作嚴格的 Token 輪換 (Rotation)
func (u *authUsecase) RefreshToken(ctx context.Context, refreshToken string) (*domain.TokenPair, error) {
	// 1. 查找此 Opaque Refresh Token 對應之 UserID (若無效或過期將直接回傳錯誤)
	userID, err := u.refreshTokenRepo.GetUserIDByRefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	// 2. 查 DB 確認 User 最新狀態 (即時防禦：確保停權/角色異動立即生效)
	user, err := u.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 3. Token Rotation: 銷毀舊的 Refresh Token，防止重放 (Replay) 攻擊
	if err := u.refreshTokenRepo.RevokeRefreshToken(ctx, refreshToken); err != nil {
		return nil, err
	}

	// 4. 簽發新 TokenPair (包含全新短效期 JWT 與全新 Opaque Refresh Token)
	newTokenPair, err := u.tokenManager.GenerateTokenPair(user)
	if err != nil {
		return nil, err
	}

	// 5. 儲存全新 Refresh Token
	if err := u.refreshTokenRepo.StoreRefreshToken(ctx, newTokenPair.RefreshToken, user.ID, u.refreshTTL); err != nil {
		return nil, err
	}

	return newTokenPair, nil
}

// Logout 登出：
//  1. 撤銷 Refresh Token —— 真正終止工作階段，之後無法再換發新的 Access Token
//  2. 將 Access Token 的 JTI 寫入黑名單 —— 讓敏感路由 (RevocationCheckMiddleware) 立即拒絕
//
// 一般唯讀路由不查黑名單，因此已簽發的 Access Token 在剩餘效期內仍可讀取，這是無狀態 JWT 的刻意取捨
func (u *authUsecase) Logout(ctx context.Context, accessToken, refreshToken string) error {
	claims, err := u.tokenManager.ValidateAccessToken(accessToken)
	if err != nil {
		return err
	}

	// 1. 撤銷 Refresh Token（先確認歸屬，避免以他人的 Token 執行登出）
	ownerID, err := u.refreshTokenRepo.GetUserIDByRefreshToken(ctx, refreshToken)
	switch {
	case errors.Is(err, domain.ErrInvalidToken):
		// 已過期或已被撤銷：視為已登出，維持冪等
	case err != nil:
		return err
	case ownerID != claims.UserID:
		return domain.ErrForbidden
	default:
		if err := u.refreshTokenRepo.RevokeRefreshToken(ctx, refreshToken); err != nil {
			return err
		}
	}

	// 2. 將 Access Token 加入黑名單，TTL 對齊剩餘效期
	remaining := time.Until(claims.ExpiresAt)
	if remaining > 0 {
		return u.blacklist.RevokeToken(ctx, claims.TokenID, remaining)
	}
	return nil
}
