package domain

import (
	"context"

	"github.com/shopspring/decimal"
)

// 帳務檢查項目
const (
	// CheckArithmetic 單筆流水：balance_after 必須等於 balance_before ± amount
	CheckArithmetic = "ARITHMETIC"
	// CheckChainBreak 前後流水：本筆的 balance_before 必須等於上一筆的 balance_after
	CheckChainBreak = "CHAIN_BREAK"
	// CheckWalletMismatch 錢包餘額必須等於最後一筆流水的 balance_after
	CheckWalletMismatch = "WALLET_MISMATCH"
)

// LedgerIssue 代表 DB 帳務內部的一筆不一致 (流水帳與錢包餘額之間)
type LedgerIssue struct {
	Check         string `db:"check_name"`
	WalletID      int64  `db:"wallet_id"`
	TransactionID int64  `db:"transaction_id"`
	Detail        string `db:"detail"`
}

// WalletSnapshot 是對帳時讀取的錢包快照
type WalletSnapshot struct {
	ID       int64           `db:"id"`
	UserID   int64           `db:"user_id"`
	Currency string          `db:"currency"`
	Balance  decimal.Decimal `db:"balance"`
	Version  int             `db:"version"`
}

// CacheIssue 代表 Redis 快取餘額與 DB 不一致 (已排除進行中的交易)
type CacheIssue struct {
	WalletID   int64
	UserID     int64
	Currency   string
	DBUnits    int64 // DB 餘額 (整數最小單位)
	CacheUnits int64 // Redis 餘額 (整數最小單位)
	Healed     bool  // 是否已刪除快取
}

// ReconcileRepository 提供對帳所需的 DB 查詢
type ReconcileRepository interface {
	// FindLedgerIssues 一次執行所有 DB 帳務檢查，最多回傳 limit 筆
	FindLedgerIssues(ctx context.Context, limit int) ([]LedgerIssue, error)
	// ListWallets 以 keyset 分頁列出錢包 (id > afterID)，依 id 排序
	ListWallets(ctx context.Context, afterID int64, limit int) ([]WalletSnapshot, error)
	// GetWalletsByIDs 重新讀取指定錢包的最新狀態
	GetWalletsByIDs(ctx context.Context, ids []int64) ([]WalletSnapshot, error)
}

// CacheInspector 提供對帳所需的快取讀取與修復
type CacheInspector interface {
	// GetCachedBalances 批次讀取快取餘額，回傳 walletID → 整數單位；快取不存在的錢包不會出現在結果中
	GetCachedBalances(ctx context.Context, wallets []WalletSnapshot) (map[int64]int64, error)
	// InvalidateWallet 刪除錢包快取，下一個請求會從 DB 重建
	InvalidateWallet(ctx context.Context, userID int64, currency string) error
}
