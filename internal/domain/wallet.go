package domain

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// 操作方向常數 (用於 Redis 快取)
const (
	OpDebit  = "DEBIT"
	OpCredit = "CREDIT"
)

// PreProcess 結果常數
const (
	ResultSuccess      = 0
	ResultDuplicateTx  = 1
	ResultInsufficient = 2
	ResultCacheMiss    = 3
)

// Wallet 代表玩家的錢包餘額
type Wallet struct {
	ID        int64           `json:"id" db:"id"`
	UserID    int64           `json:"user_id" db:"user_id"`
	Balance   decimal.Decimal `json:"balance" db:"balance"` // 使用 decimal 確保金額精準度
	Currency  string          `json:"currency" db:"currency"`
	CreatedAt time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt time.Time       `json:"updated_at" db:"updated_at"`
	Version   int             `json:"version" db:"version"` // 用於樂觀鎖控制 (Optimistic Locking)
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

// WalletRepository 定義了錢包的儲存庫介面
type WalletRepository interface {
	// 建立錢包
	CreateWallet(ctx context.Context, wallet *Wallet) error

	// 取得錢包 (不加鎖)
	GetWalletByID(ctx context.Context, id int64) (*Wallet, error)
	GetWalletByUserID(ctx context.Context, userID int64, currency string) (*Wallet, error)

	// 取得錢包 (加悲觀鎖 SELECT ... FOR UPDATE)
	GetWalletByIDForUpdate(ctx context.Context, id int64) (*Wallet, error)

	// 更新錢包餘額 (使用悲觀鎖更新或樂觀鎖更新)
	UpdateBalance(ctx context.Context, wallet *Wallet) error
}

// WalletCache 定義了快取層的介面
type WalletCache interface {
	PreProcess(ctx context.Context, op, providerID, providerTxID string, userID int64, currency string, amount float64) (int, error)
	InitCacheIfMissing(ctx context.Context, userID int64, currency string, balance float64) error
	RefundAndUnlock(ctx context.Context, userID int64, currency, providerID, providerTxID string, amount float64) error

	// 分散式快取重建鎖 (Hydration Lock)
	AcquireHydrationLock(ctx context.Context, userID int64) (bool, error)
	ReleaseHydrationLock(ctx context.Context, userID int64) error
}

// WalletUsecase 定義了錢包核心業務邏輯的介面
type WalletUsecase interface {
	// 取得玩家餘額
	GetBalance(ctx context.Context, userID int64, currency string) (*Wallet, error)

	// 處理資金變動 (包含扣款、派彩、寫入流水帳，保證 Transaction 原子性)
	ProcessTransaction(ctx context.Context, req *TransactionRequest) (*Transaction, error)
}
