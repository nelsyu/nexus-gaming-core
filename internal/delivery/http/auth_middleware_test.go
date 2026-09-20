package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	nexushttp "github.com/bosstest/nexus-core/internal/delivery/http"
	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/infrastructure/auth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type mockTokenBlacklist struct {
	revoked map[string]bool
}

func newMockTokenBlacklist() *mockTokenBlacklist {
	return &mockTokenBlacklist{revoked: make(map[string]bool)}
}

func (m *mockTokenBlacklist) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	m.revoked[jti] = true
	return nil
}

func (m *mockTokenBlacklist) IsRevoked(ctx context.Context, jti string) (bool, error) {
	return m.revoked[jti], nil
}

func setupTestRouter(tokenManager domain.TokenManager, blacklist domain.TokenBlacklist) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(nexushttp.SecurityHeadersMiddleware())

	protected := r.Group("/protected")
	protected.Use(nexushttp.AuthMiddleware(tokenManager))
	{
		protected.GET("/ping", func(c *gin.Context) {
			userID, _ := c.Get(nexushttp.CtxUserID)
			role, _ := c.Get(nexushttp.CtxRole)
			c.JSON(http.StatusOK, gin.H{
				"user_id": userID,
				"role":    role,
			})
		})

		protected.GET("/admin-only", nexushttp.RBACMiddleware(domain.RoleAdmin), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "admin_access_granted"})
		})

		protected.GET("/wallet", nexushttp.TenantGuardMiddleware(), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "wallet_access_granted"})
		})

		// 模擬敏感路由（如交易）：額外檢查黑名單
		protected.POST("/sensitive", nexushttp.RevocationCheckMiddleware(blacklist), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "sensitive_access_granted"})
		})
	}

	return r
}

func TestSecurityHeaders(t *testing.T) {
	tokenManager := auth.NewJWTManager("test-secret", time.Minute, time.Hour)
	blacklist := newMockTokenBlacklist()
	router := setupTestRouter(tokenManager, blacklist)

	req, _ := http.NewRequest(http.MethodGet, "/protected/ping", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 驗證 OWASP 安全 Header 是否完整注入
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", w.Header().Get("X-XSS-Protection"))
	assert.Equal(t, "default-src 'self'", w.Header().Get("Content-Security-Policy"))
}

func TestAuthMiddleware(t *testing.T) {
	secret := "test-secret-key-1234567890123456"
	tokenManager := auth.NewJWTManager(secret, 15*time.Minute, 24*time.Hour)
	blacklist := newMockTokenBlacklist()
	router := setupTestRouter(tokenManager, blacklist)

	playerUser := &domain.User{
		ID:       101,
		TenantID: "TENANT_A",
		Username: "player_one",
		Role:     domain.RolePlayer,
	}
	tokenPair, err := tokenManager.GenerateTokenPair(playerUser)
	assert.NoError(t, err)

	t.Run("Missing Authorization Header -> 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/ping", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Malformed Authorization Header -> 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/ping", nil)
		req.Header.Set("Authorization", "InvalidHeader "+tokenPair.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Valid Token -> 200", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/ping", nil)
		req.Header.Set("Authorization", "Bearer "+tokenPair.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Revoked Token: stateless route still passes, sensitive route -> 401", func(t *testing.T) {
		claims, err := tokenManager.ValidateAccessToken(tokenPair.AccessToken)
		assert.NoError(t, err)

		// 模擬使用者登出，將 JTI 放入黑名單
		err = blacklist.RevokeToken(context.Background(), claims.TokenID, time.Minute)
		assert.NoError(t, err)

		// 一般路由：純驗簽不查黑名單，在剩餘效期內仍可通過（刻意的取捨）
		req, _ := http.NewRequest(http.MethodGet, "/protected/ping", nil)
		req.Header.Set("Authorization", "Bearer "+tokenPair.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		// 敏感路由：即時阻斷
		req, _ = http.NewRequest(http.MethodPost, "/protected/sensitive", nil)
		req.Header.Set("Authorization", "Bearer "+tokenPair.AccessToken)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Non-revoked Token on sensitive route -> 200", func(t *testing.T) {
		fresh, err := tokenManager.GenerateTokenPair(playerUser)
		assert.NoError(t, err)

		req, _ := http.NewRequest(http.MethodPost, "/protected/sensitive", nil)
		req.Header.Set("Authorization", "Bearer "+fresh.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Opaque Refresh Token used as Access Token -> 401 Unauthorized (Type Confusion Prevention)", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/ping", nil)
		req.Header.Set("Authorization", "Bearer "+tokenPair.RefreshToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestRBACMiddleware(t *testing.T) {
	secret := "test-secret-key-1234567890123456"
	tokenManager := auth.NewJWTManager(secret, 15*time.Minute, 24*time.Hour)
	blacklist := newMockTokenBlacklist()
	router := setupTestRouter(tokenManager, blacklist)

	player := &domain.User{ID: 101, TenantID: "TENANT_A", Username: "player", Role: domain.RolePlayer}
	admin := &domain.User{ID: 999, TenantID: "TENANT_A", Username: "admin", Role: domain.RoleAdmin}

	playerTokens, _ := tokenManager.GenerateTokenPair(player)
	adminTokens, _ := tokenManager.GenerateTokenPair(admin)

	t.Run("Player accesses Admin endpoint -> 403 Forbidden", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/admin-only", nil)
		req.Header.Set("Authorization", "Bearer "+playerTokens.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("Admin accesses Admin endpoint -> 200 OK", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/admin-only", nil)
		req.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestTenantGuard_IDOR_Prevention(t *testing.T) {
	secret := "test-secret-key-1234567890123456"
	tokenManager := auth.NewJWTManager(secret, 15*time.Minute, 24*time.Hour)
	blacklist := newMockTokenBlacklist()
	router := setupTestRouter(tokenManager, blacklist)

	player1 := &domain.User{ID: 101, TenantID: "TENANT_A", Username: "player1", Role: domain.RolePlayer}
	admin := &domain.User{ID: 999, TenantID: "TENANT_A", Username: "admin", Role: domain.RoleAdmin}

	player1Tokens, _ := tokenManager.GenerateTokenPair(player1)
	adminTokens, _ := tokenManager.GenerateTokenPair(admin)

	t.Run("Player accesses own wallet (user_id=101) -> 200 OK", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/wallet?user_id=101", nil)
		req.Header.Set("Authorization", "Bearer "+player1Tokens.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Player attempts IDOR / BOLA on another user (user_id=102) -> 403 Forbidden", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/wallet?user_id=102", nil)
		req.Header.Set("Authorization", "Bearer "+player1Tokens.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("Admin accesses player's wallet (user_id=101) -> 200 OK", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/protected/wallet?user_id=101", nil)
		req.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}
