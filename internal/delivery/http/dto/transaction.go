package dto

import (
	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/shopspring/decimal"
)

// TransactionRequest 定義 HTTP 層專用的交易請求 DTO
// 職責：處理 HTTP 格式驗證（必填、長度、格式）
type TransactionRequest struct {
	UserID       int64                  `json:"user_id" validate:"required,gt=0"`
	Currency     string                 `json:"currency" validate:"required,len=3,uppercase"`
	Type         domain.TransactionType `json:"type" validate:"required,transaction_type"`
	Amount       string                 `json:"amount" validate:"required,decimal_positive"`
	ProviderID   string                 `json:"provider_id" validate:"required,min=2,max=50,alphanum_underscore"`
	ProviderTxID string                 `json:"provider_tx_id" validate:"required,min=1,max=100"`
	ReferenceID  string                 `json:"reference_id" validate:"omitempty,max=100"`

	// BET / WIN 必填 round_id、REFUND 必填 related_provider_tx_id：
	// 「哪種類型必填哪個欄位」屬於業務規則，由 usecase 檢查，HTTP 層只驗格式。
	RoundID             string `json:"round_id" validate:"omitempty,max=100"`
	RelatedProviderTxID string `json:"related_provider_tx_id" validate:"omitempty,max=100"`
}

// ToDomain 將 HTTP DTO 轉換為 Domain Request
// 這裡處理類型轉換（string → decimal.Decimal）
func (r *TransactionRequest) ToDomain() (*domain.TransactionRequest, error) {
	amount, err := decimal.NewFromString(r.Amount)
	if err != nil {
		return nil, domain.ErrInvalidAmount
	}

	return &domain.TransactionRequest{
		UserID:       r.UserID,
		Currency:     r.Currency,
		Type:         r.Type,
		Amount:       amount,
		ProviderID:   r.ProviderID,
		ProviderTxID: r.ProviderTxID,
		ReferenceID:  r.ReferenceID,

		RoundID:             r.RoundID,
		RelatedProviderTxID: r.RelatedProviderTxID,
	}, nil
}

// AdminTransactionRequest 後台人工存提款的請求 DTO。
// 刻意沒有 provider_id (由伺服器依租戶決定)，也沒有遊戲局相關欄位。
type AdminTransactionRequest struct {
	UserID       int64                  `json:"user_id" validate:"required,gt=0"`
	Currency     string                 `json:"currency" validate:"required,len=3,uppercase"`
	Type         domain.TransactionType `json:"type" validate:"required,transaction_type"`
	Amount       string                 `json:"amount" validate:"required,decimal_positive"`
	ProviderTxID string                 `json:"provider_tx_id" validate:"required,min=1,max=100"`
	ReferenceID  string                 `json:"reference_id" validate:"omitempty,max=100"`
}

// ToDomain providerID 必須是伺服器產生的值
func (r *AdminTransactionRequest) ToDomain(providerID string) (*domain.TransactionRequest, error) {
	amount, err := decimal.NewFromString(r.Amount)
	if err != nil {
		return nil, domain.ErrInvalidAmount
	}

	return &domain.TransactionRequest{
		UserID:       r.UserID,
		Currency:     r.Currency,
		Type:         r.Type,
		Amount:       amount,
		ProviderID:   providerID,
		ProviderTxID: r.ProviderTxID,
		ReferenceID:  r.ReferenceID,
	}, nil
}

// GetBalanceRequest 定義查詢餘額的請求 DTO
type GetBalanceRequest struct {
	UserID   int64  `form:"user_id" validate:"required,gt=0"`
	Currency string `form:"currency" validate:"required,len=3,uppercase"`
}
