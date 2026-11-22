package domain

import (
	"context"
	"time"
)

// UserClaims 封裝身分憑證資訊（純 Domain 結構，不依賴外部 JWT 套件）
type UserClaims struct {
	UserID    int64     `json:"user_id"`
	TenantID  string    `json:"tenant_id"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	TokenID   string    `json:"jti"`
	ExpiresAt time.Time `json:"exp"`
	IssuedAt  time.Time `json:"iat"`
}

// TokenPair 代表簽發給前端的雙 Token
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // Access Token 存活秒數
	TokenType    string `json:"token_type"` // 固定為 "Bearer"
}

// RegisterRequest 公開註冊的業務參數。
// 刻意沒有 Role 欄位：公開註冊只能建立玩家，角色不能由呼叫者決定。
type RegisterRequest struct {
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
	Password string `json:"password"`
	Currency string `json:"currency"`
}

// LoginRequest 登入業務參數
type LoginRequest struct {
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// PasswordHasher 定義密碼雜湊抽象介面
type PasswordHasher interface {
	HashPassword(password string) (string, error)
	ComparePassword(hashedPassword, password string) error
}

// TokenManager 定義 Token 簽發與 Access Token 驗證抽象介面
type TokenManager interface {
	GenerateTokenPair(user *User) (*TokenPair, error)
	ValidateAccessToken(tokenString string) (*UserClaims, error)
}

// RefreshTokenRepository 定義 Opaque Refresh Token 在快取儲存中的管理介面
type RefreshTokenRepository interface {
	StoreRefreshToken(ctx context.Context, token string, userID int64, ttl time.Duration) error
	GetUserIDByRefreshToken(ctx context.Context, token string) (int64, error)
	RevokeRefreshToken(ctx context.Context, token string) error
}

// TokenBlacklist 定義基於快取的 Token 即時吊銷黑名單介面
type TokenBlacklist interface {
	RevokeToken(ctx context.Context, jti string, ttl time.Duration) error
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

// AuthUsecase 定義認證業務邏輯介面
type AuthUsecase interface {
	Register(ctx context.Context, req *RegisterRequest) (*User, error)
	Login(ctx context.Context, req *LoginRequest) (*TokenPair, error)
	RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error)
	// Logout 撤銷 Refresh Token（終止工作階段）並將 Access Token 加入黑名單（阻斷敏感操作）
	Logout(ctx context.Context, accessToken, refreshToken string) error
}
