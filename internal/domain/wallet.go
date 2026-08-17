package domain

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// Wallet 代表玩家的錢包餘額
type Wallet struct {
	ID        int64           `json:"id"`
	UserID    int64           `json:"user_id"`
	Balance   decimal.Decimal `json:"balance"` // 使用 decimal 確保金額精準度
	Currency  string          `json:"currency"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Version   int             `json:"version"` // 用於樂觀鎖控制 (Optimistic Locking)
}

// TransactionRequest 代表一筆由遊戲商或外部傳來的資金變動請求
type TransactionRequest struct {
	UserID       int64           `json:"user_id"`
	Currency     string          `json:"currency"`
	Type         TransactionType `json:"type"`           // BET, WIN, REFUND 等
	Amount       decimal.Decimal `json:"amount"`         // 必須為正數
	ProviderID   string          `json:"provider_id"`    // 第三方遊戲商 ID
	ProviderTxID string          `json:"provider_tx_id"` // 遊戲商提供的唯一交易序號 (用於防重)
	ReferenceID  string          `json:"reference_id"`   // 其他關聯 ID (例如 Round ID)
}

// WalletUsecase 定義了錢包核心業務邏輯的介面
type WalletUsecase interface {
	// 取得玩家餘額
	GetBalance(ctx context.Context, userID int64, currency string) (*Wallet, error)

	// 處理資金變動 (包含扣款、派彩、寫入流水帳，保證 Transaction 原子性)
	ProcessTransaction(ctx context.Context, req *TransactionRequest) (*Transaction, error)
}
