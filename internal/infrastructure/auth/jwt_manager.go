package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type customClaims struct {
	UserID    int64       `json:"user_id"`
	TenantID  string      `json:"tenant_id"`
	Username  string      `json:"username"`
	Role      domain.Role `json:"role"`
	TokenType string      `json:"token_type"` // 必須為 "access"，杜絕 Token 混淆攻擊
	jwt.RegisteredClaims
}

// JWTManager 負責產生 Access Token (JWT) 與 Opaque Refresh Token，並驗證 Access Token
type JWTManager struct {
	secretKey       []byte
	accessDuration  time.Duration
	refreshDuration time.Duration
	issuer          string
}

func NewJWTManager(secretKey string, accessDuration, refreshDuration time.Duration) *JWTManager {
	if accessDuration <= 0 {
		// 無狀態路由不查黑名單，此值即為撤銷生效的最大延遲
		accessDuration = 5 * time.Minute
	}
	if refreshDuration <= 0 {
		refreshDuration = 7 * 24 * time.Hour
	}
	return &JWTManager{
		secretKey:       []byte(secretKey),
		accessDuration:  accessDuration,
		refreshDuration: refreshDuration,
		issuer:          "nexus-gaming-core",
	}
}

// GenerateTokenPair 簽發短效期 Access Token (JWT) 與高熵長效期 Refresh Token (Opaque Token)
func (m *JWTManager) GenerateTokenPair(user *domain.User) (*domain.TokenPair, error) {
	now := time.Now()

	// 1. 產生 Access Token (短效期，帶完整 User Claims 與 TokenType: "access")
	accessJTI := uuid.New().String()
	accessClaims := customClaims{
		UserID:    user.ID,
		TenantID:  user.TenantID,
		Username:  user.Username,
		Role:      user.Role,
		TokenType: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        accessJTI,
			Issuer:    m.issuer,
			Subject:   fmt.Sprintf("%d", user.ID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessDuration)),
		},
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	signedAccessToken, err := accessToken.SignedString(m.secretKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// 2. 產生 Opaque Refresh Token (256-bit 密碼學安全隨機字串，杜絕個資外洩與 Token 混淆)
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("failed to generate secure random refresh token: %w", err)
	}
	opaqueRefreshToken := hex.EncodeToString(randomBytes)

	return &domain.TokenPair{
		AccessToken:  signedAccessToken,
		RefreshToken: opaqueRefreshToken,
		ExpiresIn:    int64(m.accessDuration.Seconds()),
		TokenType:    "Bearer",
	}, nil
}

// ValidateAccessToken 驗證 Access Token 並檢查 token_type 防止混淆
func (m *JWTManager) ValidateAccessToken(tokenString string) (*domain.UserClaims, error) {
	return m.parseAndValidate(tokenString)
}

func (m *JWTManager) parseAndValidate(tokenString string) (*domain.UserClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &customClaims{}, func(token *jwt.Token) (any, error) {
		// 嚴格防止 None Algorithm 攻擊與對稱/非對稱混淆
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.secretKey, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, domain.ErrTokenExpired
		}
		return nil, domain.ErrInvalidToken
	}

	claims, ok := token.Claims.(*customClaims)
	if !ok || !token.Valid {
		return nil, domain.ErrInvalidToken
	}

	// 資安防護：強制檢查 Token 類型必須為 "access"
	if claims.TokenType != "access" {
		return nil, domain.ErrInvalidToken
	}

	return &domain.UserClaims{
		UserID:    claims.UserID,
		TenantID:  claims.TenantID,
		Username:  claims.Username,
		Role:      claims.Role,
		TokenID:   claims.ID,
		ExpiresAt: claims.ExpiresAt.Time,
		IssuedAt:  claims.IssuedAt.Time,
	}, nil
}
