package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/gin-gonic/gin"
)

type WalletHandler struct {
	walletUsecase domain.WalletUsecase
}

func NewWalletHandler(wu domain.WalletUsecase) *WalletHandler {
	return &WalletHandler{
		walletUsecase: wu,
	}
}

// GetBalance 查詢玩家錢包餘額
func (h *WalletHandler) GetBalance(c *gin.Context) {
	userIDStr := c.Query("user_id")
	currency := c.Query("currency")

	if userIDStr == "" || currency == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing user_id or currency"})
		return
	}

	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
		return
	}

	wallet, err := h.walletUsecase.GetBalance(c.Request.Context(), userID, currency)
	if err != nil {
		if errors.Is(err, domain.ErrWalletNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
			return
		}
		c.Error(err) // 將詳細錯誤加入 Gin 的 Context 中，讓 LoggerMiddleware 紀錄
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": wallet,
	})
}

// ProcessTransaction 處理第三方遊戲商扣款/派彩
func (h *WalletHandler) ProcessTransaction(c *gin.Context) {
	var req domain.TransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}

	tx, err := h.walletUsecase.ProcessTransaction(c.Request.Context(), &req)
	if err != nil {
		if errors.Is(err, domain.ErrInsufficientFunds) {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": "insufficient funds"})
			return
		}
		if errors.Is(err, domain.ErrWalletNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
			return
		}
		c.Error(err) // 將詳細錯誤加入 Gin 的 Context 中，讓 LoggerMiddleware 紀錄
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": tx,
	})
}
