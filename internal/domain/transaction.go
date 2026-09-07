package domain

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// TransactionType 定義資金流動的類型
type TransactionType string

const (
	TxTypeBet      TransactionType = "BET"      // 下注 (扣款)
	TxTypeWin      TransactionType = "WIN"      // 派彩 (加款)
	TxTypeRefund   TransactionType = "REFUND"   // 退款/沖正 (加款或扣款)
	TxTypeDeposit  TransactionType = "DEPOSIT"  // 充值 (加款)
	TxTypeWithdraw TransactionType = "WITHDRAW" // 提現 (扣款)
)

// Transaction 代表一筆錢包餘額的變動 (Ledger Entry)
type Transaction struct {
	ID            int64           `json:"id" db:"id"`
	WalletID      int64           `json:"wallet_id" db:"wallet_id"`
	Type          TransactionType `json:"type" db:"type"`
	Amount        decimal.Decimal `json:"amount" db:"amount"`         // 變動金額 (正負數)
	BalanceBefore decimal.Decimal `json:"balance_before" db:"balance_before"` // 變動前餘額
	BalanceAfter  decimal.Decimal `json:"balance_after" db:"balance_after"`  // 變動後餘額
	ProviderID    string          `json:"provider_id" db:"provider_id"`    // 第三方遊戲商ID (e.g., "PG_SOFT")
	ProviderTxID  string          `json:"provider_tx_id" db:"provider_tx_id"` // 第三方遊戲商傳來的唯一交易序號 (用於防重)
	ReferenceID   string          `json:"reference_id" db:"reference_id"`   // 關聯的注單ID或其他參考ID
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
}

// TransactionRepository 定義了交易流水帳的儲存庫介面
type TransactionRepository interface {
	// 新增一筆流水帳
	CreateTransaction(ctx context.Context, tx *Transaction) error

	// 透過第三方交易 ID 查詢是否已存在 (防重檢查)
	GetByProviderTxID(ctx context.Context, providerID, providerTxID string) (*Transaction, error)
}
