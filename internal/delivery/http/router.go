package http

import (
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RouterDeps 組裝路由所需的依賴與設定
type RouterDeps struct {
	WalletHandler *WalletHandler
	AuthHandler   *AuthHandler
	TokenManager  domain.TokenManager
	Blacklist     domain.TokenBlacklist
	ProviderAuth  domain.ProviderAuthenticator

	// TrustedProxies 可信任的反向代理 / 負載平衡器 IP 或 CIDR。
	// 只有來自這些位址的請求，才會採信 X-Forwarded-For 作為客戶端 IP。
	// 空值 = 不信任任何代理，客戶端 IP 一律取 TCP 連線來源。
	// 若誤設為信任全部，任何人都能偽造 X-Forwarded-For 繞過 IP 白名單。
	TrustedProxies []string
}

// SetupRouter 註冊所有的 API 路由
func SetupRouter(deps RouterDeps) (*gin.Engine, error) {
	r := gin.New()

	// gin 預設信任所有代理 (會直接採信 X-Forwarded-For)，必須明確設定
	if err := r.SetTrustedProxies(deps.TrustedProxies); err != nil {
		return nil, fmt.Errorf("invalid trusted proxies: %w", err)
	}

	// 註冊全域 Middleware
	r.Use(gin.Recovery())              // 捕捉 Panic
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
			authGroup.POST("/register", deps.AuthHandler.Register)
			authGroup.POST("/login", deps.AuthHandler.Login)
			authGroup.POST("/refresh", deps.AuthHandler.RefreshToken)

			// 受保護端點：登出需要 Access Token 以識別身分（純驗簽即可，重複登出為冪等操作）
			authProtected := authGroup.Group("")
			authProtected.Use(AuthMiddleware(deps.TokenManager))
			{
				authProtected.POST("/logout", deps.AuthHandler.Logout)
			}
		}

		// 錢包路由群組：玩家與營運後台 (JWT 驗證)
		walletGroup := apiV1.Group("/wallet")
		walletGroup.Use(AuthMiddleware(deps.TokenManager))
		{
			// 取得餘額：唯讀操作，不查黑名單。
			// 「能看誰的餘額」在 handler 綁定參數之後才判斷，確保檢查的值就是實際查詢的值
			walletGroup.GET("/balance", deps.WalletHandler.GetBalance)

			// 人工存提款：涉及資金異動，額外檢查 Token 是否已被吊銷（即時阻斷）
			// 僅限營運方 (Operator) 或系統管理員 (Admin)，只允許 DEPOSIT / WITHDRAW，且只能對同租戶的使用者操作
			walletGroup.POST("/transaction",
				RevocationCheckMiddleware(deps.Blacklist),
				RBACMiddleware(domain.RoleAdmin, domain.RoleOperator),
				deps.WalletHandler.ProcessAdminTransaction,
			)
		}

		// 遊戲商路由群組：系統對系統呼叫 (HMAC 簽章驗證，不使用 JWT)
		// 只允許 BET / WIN / REFUND
		providerGroup := apiV1.Group("/provider")
		providerGroup.Use(ProviderAuthMiddleware(deps.ProviderAuth))
		{
			providerGroup.POST("/transaction", deps.WalletHandler.ProcessProviderTransaction)
		}
	}

	return r, nil
}
