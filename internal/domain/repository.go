package domain

import (
	"context"
)

// Repository 定義了所有的儲存庫介面
// 為了支援資料庫 Transaction (交易)，我們在方法中傳入 context.Context
// 實作層可以透過 context 提取 tx

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

type TransactionRepository interface {
	// 新增一筆流水帳
	CreateTransaction(ctx context.Context, tx *Transaction) error
	
	// 透過第三方交易 ID 查詢是否已存在 (防重檢查)
	GetByProviderTxID(ctx context.Context, providerID, providerTxID string) (*Transaction, error)
}

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

// WalletCache 定義了快取層的介面
type WalletCache interface {
	PreProcess(ctx context.Context, op, providerID, providerTxID string, userID int64, currency string, amount float64) (int, error)
	InitCacheIfMissing(ctx context.Context, userID int64, currency string, balance float64) error
	RefundAndUnlock(ctx context.Context, userID int64, currency, providerID, providerTxID string, amount float64) error

	// 分散式快取重建鎖 (Hydration Lock)
	AcquireHydrationLock(ctx context.Context, userID int64) (bool, error)
	ReleaseHydrationLock(ctx context.Context, userID int64) error
}

// UnitOfWork 封裝了資料庫交易邊界
type UnitOfWork interface {
	DoTx(ctx context.Context, fn func(txCtx context.Context) error) error
}
