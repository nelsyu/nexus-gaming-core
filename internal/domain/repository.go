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
