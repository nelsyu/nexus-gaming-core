package http

import (
	"errors"

	"github.com/bosstest/nexus-core/internal/delivery/http/dto"
	"github.com/bosstest/nexus-core/internal/delivery/http/response"
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
	// 使用 ShouldBindQuery 自動驗證 query parameters
	var req dto.GetBalanceRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ValidationError(c, err)
		return
	}

	wallet, err := h.walletUsecase.GetBalance(c.Request.Context(), req.UserID, req.Currency)
	if err != nil {
		if errors.Is(err, domain.ErrWalletNotFound) {
			response.NotFound(c, "wallet not found")
			return
		}
		response.InternalServerError(c, "internal server error")
		return
	}

	response.Success(c, wallet)
}

// ProcessTransaction 處理第三方遊戲商扣款/派彩
func (h *WalletHandler) ProcessTransaction(c *gin.Context) {
	// 1. HTTP 層驗證：格式、必填、長度
	var req dto.TransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, err)
		return
	}

	// 2. 轉換為 Domain Request
	domainReq, err := req.ToDomain()
	if err != nil {
		response.BadRequest(c, "invalid amount format")
		return
	}

	// 3. 執行業務邏輯
	tx, err := h.walletUsecase.ProcessTransaction(c.Request.Context(), domainReq)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidAmount) {
			response.BadRequest(c, err.Error())
			return
		}
		if errors.Is(err, domain.ErrInsufficientFunds) {
			response.PaymentRequired(c, "insufficient funds")
			return
		}
		if errors.Is(err, domain.ErrWalletNotFound) {
			response.NotFound(c, "wallet not found")
			return
		}
		response.InternalServerError(c, "internal server error")
		return
	}

	response.Success(c, tx)
}
