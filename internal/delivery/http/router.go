package http

import (
	"github.com/gin-gonic/gin"
)

// SetupRouter 註冊所有的 API 路由
func SetupRouter(walletHandler *WalletHandler) *gin.Engine {
	r := gin.Default()

	// 簡單的健康檢查端點
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	apiV1 := r.Group("/api/v1")
	{
		walletGroup := apiV1.Group("/wallet")
		{
			// 取得餘額：GET /api/v1/wallet/balance?user_id=1&currency=TWD
			walletGroup.GET("/balance", walletHandler.GetBalance)

			// 處理交易：POST /api/v1/wallet/transaction
			walletGroup.POST("/transaction", walletHandler.ProcessTransaction)
		}
	}

	return r
}
