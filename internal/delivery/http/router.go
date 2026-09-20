package http

import (
	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// SetupRouter 註冊所有的 API 路由
func SetupRouter(
	walletHandler *WalletHandler,
	authHandler *AuthHandler,
	tokenManager domain.TokenManager,
	blacklist domain.TokenBlacklist,
) *gin.Engine {
	r := gin.New()

	// 註冊全域 Middleware
	r.Use(gin.Recovery()) // 捕捉 Panic
	r.Use(SecurityHeadersMiddleware()) // 注入 OWASP 安全標頭
	r.Use(TraceMiddleware())
	r.Use(LoggerMiddleware())
	r.Use(MetricsMiddleware())

	// Prometheus Metrics 端點 (公開)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 簡單的健康檢查端點 (公開)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	apiV1 := r.Group("/api/v1")
	{
		// 認證路由群組
		authGroup := apiV1.Group("/auth")
		{
			// 公開端點
			authGroup.POST("/register", authHandler.Register)
			authGroup.POST("/login", authHandler.Login)
			authGroup.POST("/refresh", authHandler.RefreshToken)

			// 受保護端點：登出需要 Access Token 以識別身分（純驗簽即可，重複登出為冪等操作）
			authProtected := authGroup.Group("")
			authProtected.Use(AuthMiddleware(tokenManager))
			{
				authProtected.POST("/logout", authHandler.Logout)
			}
		}

		// 錢包路由群組 (均受 JWT 無狀態驗證防護)
		walletGroup := apiV1.Group("/wallet")
		walletGroup.Use(AuthMiddleware(tokenManager))
		{
			// 取得餘額：唯讀操作，不查黑名單以保持無狀態；受 TenantGuard 防護，禁止跨玩家/跨租戶橫向越權 (IDOR 防範)
			walletGroup.GET("/balance", TenantGuardMiddleware(), walletHandler.GetBalance)

			// 處理交易：涉及資金異動，額外檢查 Token 是否已被吊銷（即時阻斷）
			// 僅限營運方 (Operator) 或系統管理員 (Admin)
			walletGroup.POST("/transaction",
				RevocationCheckMiddleware(blacklist),
				RBACMiddleware(domain.RoleAdmin, domain.RoleOperator),
				walletHandler.ProcessTransaction,
			)
		}
	}

	return r
}
