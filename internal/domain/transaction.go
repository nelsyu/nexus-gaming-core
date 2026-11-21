package domain

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// TransactionType 定義資金流動的類型
type TransactionType string

const (
	TxTypeBet      TransactionType = "BET"      // 下注 (扣款)，必須帶 round_id
	TxTypeWin      TransactionType = "WIN"      // 派彩 (加款)，同一局必須有未退款的 BET
	TxTypeRefund   TransactionType = "REFUND"   // 退款 (加款)，必須指向原始 BET，金額以原注單為準
	TxTypeDeposit  TransactionType = "DEPOSIT"  // 充值 (加款)
	TxTypeWithdraw TransactionType = "WITHDRAW" // 提現 (扣款)
)

// Transaction 代表一筆錢包餘額的變動 (Ledger Entry)
type Transaction struct {
	ID                  int64           `json:"id" db:"id"`
	WalletID            int64           `json:"wallet_id" db:"wallet_id"`
	Type                TransactionType `json:"type" db:"type"`
	Amount              decimal.Decimal `json:"amount" db:"amount"`                                 // 變動金額 (正數，方向由 Type 決定)
	BalanceBefore       decimal.Decimal `json:"balance_before" db:"balance_before"`                 // 變動前餘額
	BalanceAfter        decimal.Decimal `json:"balance_after" db:"balance_after"`                   // 變動後餘額
	ProviderID          string          `json:"provider_id" db:"provider_id"`                       // 第三方遊戲商ID (e.g., "PG_SOFT")
	ProviderTxID        string          `json:"provider_tx_id" db:"provider_tx_id"`                 // 第三方遊戲商傳來的唯一交易序號 (用於防重)
	ReferenceID         string          `json:"reference_id" db:"reference_id"`                     // 其他自由參考ID
	RoundID             string          `json:"round_id,omitempty" db:"round_id"`                   // 遊戲局ID：BET / WIN / REFUND 用來關聯同一局
	RelatedProviderTxID string          `json:"related_provider_tx_id,omitempty" db:"related_provider_tx_id"` // REFUND 指向的原始 BET 交易序號
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`
}

// TransactionRepository 定義了交易流水帳的儲存庫介面
type TransactionRepository interface {
	// CreateTransaction 新增一筆流水帳。
	// 交易序號重複回傳 ErrDuplicateTransaction；同一筆 BET 被第二次退款回傳 ErrBetAlreadyRefunded。
	CreateTransaction(ctx context.Context, tx *Transaction) error

	// GetByProviderTxID 透過第三方交易 ID 查詢 (找不到回傳 nil, nil)
	GetByProviderTxID(ctx context.Context, providerID, providerTxID string) (*Transaction, error)

	// GetRefundByTarget 查詢指向某筆 BET 的退款紀錄 (找不到回傳 nil, nil)
	GetRefundByTarget(ctx context.Context, providerID, betProviderTxID string) (*Transaction, error)

	// HasActiveBetInRound 該錢包在這一局是否有「尚未被退款」的 BET
	HasActiveBetInRound(ctx context.Context, walletID int64, providerID, roundID string) (bool, error)

	// HasWinInRound 該錢包在這一局是否已經派彩過
	HasWinInRound(ctx context.Context, walletID int64, providerID, roundID string) (bool, error)

	// VoidBet 記錄一筆作廢注單 (tombstone)，重複記錄不報錯
	VoidBet(ctx context.Context, walletID int64, providerID, betProviderTxID, voidedByProviderTxID string) error

	// IsBetVoided 這筆 BET 是否已被作廢 (退款先於下注到達)
	IsBetVoided(ctx context.Context, providerID, betProviderTxID string) (bool, error)
}
