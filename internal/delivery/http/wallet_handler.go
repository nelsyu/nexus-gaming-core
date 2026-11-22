package http

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/bosstest/nexus-core/internal/delivery/http/dto"
	"github.com/bosstest/nexus-core/internal/delivery/http/response"
	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type WalletHandler struct {
	walletUsecase domain.WalletUsecase
	authorizer    domain.WalletAuthorizer
}

func NewWalletHandler(wu domain.WalletUsecase, authorizer domain.WalletAuthorizer) *WalletHandler {
	return &WalletHandler{
		walletUsecase: wu,
		authorizer:    authorizer,
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

	// 必須用綁定後的 req.UserID 判斷：若另外解析一次 query，兩邊解析規則不同時 (例如前後空白) 就能繞過
	if err := h.authorizer.AuthorizeRead(c.Request.Context(), actorFromContext(c), req.UserID); err != nil {
		respondAccessError(c, err)
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

// 職責分離：遊戲商只能下注/派彩/退款，營運後台只能存提款
var (
	providerTxTypes = []domain.TransactionType{domain.TxTypeBet, domain.TxTypeWin, domain.TxTypeRefund}
	adminTxTypes    = []domain.TransactionType{domain.TxTypeDeposit, domain.TxTypeWithdraw}
)

// ProcessProviderTransaction 遊戲商呼叫的下注/派彩/退款 (需通過 ProviderAuthMiddleware)
func (h *WalletHandler) ProcessProviderTransaction(c *gin.Context) {
	// 1. HTTP 層驗證：格式、必填、長度
	var req dto.TransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, err)
		return
	}
	if !requireAllowedType(c, req.Type, providerTxTypes) {
		return
	}

	// 簽章只證明「請求是遊戲商 A 發的」；還要確認 A 沒有在 body 裡冒用遊戲商 B 的名義
	if req.ProviderID != c.GetString(CtxProviderID) {
		response.Forbidden(c, "provider_id does not match the authenticated provider")
		return
	}

	// 2. 轉換為 Domain Request
	domainReq, err := req.ToDomain()
	if err != nil {
		response.BadRequest(c, "invalid amount format")
		return
	}

	h.execute(c, domainReq)
}

// ProcessAdminTransaction 營運後台的人工存款/提款 (需通過 JWT、RBAC 與租戶檢查)
func (h *WalletHandler) ProcessAdminTransaction(c *gin.Context) {
	var req dto.AdminTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ValidationError(c, err)
		return
	}
	if !requireAllowedType(c, req.Type, adminTxTypes) {
		return
	}

	actor := actorFromContext(c)
	if err := h.authorizer.AuthorizeAdjust(c.Request.Context(), actor, req.UserID); err != nil {
		respondAccessError(c, err)
		return
	}
	providerID, err := domain.BackofficeProviderID(actor.TenantID)
	if err != nil {
		respondAccessError(c, err)
		return
	}

	domainReq, err := req.ToDomain(providerID)
	if err != nil {
		response.BadRequest(c, "invalid amount format")
		return
	}

	h.execute(c, domainReq)
}

func requireAllowedType(c *gin.Context, txType domain.TransactionType, allowed []domain.TransactionType) bool {
	if slices.Contains(allowed, txType) {
		return true
	}
	response.BadRequest(c, fmt.Sprintf("transaction type %s is not allowed on this endpoint", txType))
	return false
}

func respondAccessError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrForbidden):
		response.Forbidden(c, "forbidden")
	case errors.Is(err, domain.ErrWalletNotFound):
		response.NotFound(c, "wallet not found")
	default:
		logger.WithTraceID(c.Request.Context()).Error("Wallet authorization failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, response.ErrorResponse{Error: "internal server error"})
	}
}

func (h *WalletHandler) execute(c *gin.Context, domainReq *domain.TransactionRequest) {
	// 3. 執行業務邏輯
	tx, err := h.walletUsecase.ProcessTransaction(c.Request.Context(), domainReq)
	if err != nil {
		switch {
		// 400：請求本身有問題，修正後才能重送
		case errors.Is(err, domain.ErrInvalidAmount),
			errors.Is(err, domain.ErrRoundIDRequired),
			errors.Is(err, domain.ErrRefundTargetRequired),
			errors.Is(err, domain.ErrInvalidRefundTarget):
			response.BadRequest(c, err.Error())
		case errors.Is(err, domain.ErrInsufficientFunds):
			response.PaymentRequired(c, "insufficient funds")
		// 404：找不到錢包，或退款找不到原注單 (已記錄作廢)
		case errors.Is(err, domain.ErrWalletNotFound):
			response.NotFound(c, "wallet not found")
		case errors.Is(err, domain.ErrRefundTargetNotFound):
			response.NotFound(c, err.Error())
		// 409：與既有狀態衝突 (注單已作廢、已派彩、該局沒有下注)
		case errors.Is(err, domain.ErrBetVoided),
			errors.Is(err, domain.ErrBetAlreadySettled),
			errors.Is(err, domain.ErrWinWithoutBet):
			response.Conflict(c, err.Error())
		default:
			response.InternalServerError(c, "internal server error")
		}
		return
	}

	response.Success(c, tx)
}
