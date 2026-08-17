package usecase

import (
	"context"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/repository/postgres"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type walletUsecase struct {
	db      *sqlx.DB
	walletR domain.WalletRepository
	txR     domain.TransactionRepository
}

// NewWalletUsecase 建立 WalletUsecase 實例
func NewWalletUsecase(
	db *sqlx.DB,
	walletR domain.WalletRepository,
	txR domain.TransactionRepository,
) domain.WalletUsecase {
	return &walletUsecase{
		db:      db,
		walletR: walletR,
		txR:     txR,
	}
}

func (u *walletUsecase) GetBalance(ctx context.Context, userID int64, currency string) (*domain.Wallet, error) {
	// 直接從資料庫取得餘額，不需要加鎖
	return u.walletR.GetWalletByUserID(ctx, userID, currency)
}

func (u *walletUsecase) ProcessTransaction(ctx context.Context, req *domain.TransactionRequest) (*domain.Transaction, error) {
	// 1. 基本防呆檢查
	if req.Amount.LessThan(decimal.Zero) {
		return nil, fmt.Errorf("amount must be positive")
	}

	// 2. 檢查冪等性 (是否已經處理過該筆遊戲商的交易)
	// 這邊先不加鎖查詢，如果存在直接返回 (Fast path)
	existingTx, err := u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
	if err != nil {
		return nil, fmt.Errorf("check existing transaction failed: %w", err)
	}
	if existingTx != nil {
		// 已經處理過，直接回傳舊的紀錄，確保冪等性
		return existingTx, nil
	}

	// 3. 開啟資料庫事務 (DB Transaction)
	sqlxTx, err := u.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction failed: %w", err)
	}
	defer sqlxTx.Rollback() // 如果後續有 commit，此行會被忽略

	// 將 sqlxTx 注入到 context 中
	txCtx := postgres.InjectTx(ctx, sqlxTx)

	// 4. 取得錢包並加上悲觀鎖 (SELECT ... FOR UPDATE)
	// 確保同一個玩家在同一時間只有一個請求能修改餘額
	wallet, err := u.walletR.GetWalletByUserID(txCtx, req.UserID, req.Currency)
	if err != nil {
		return nil, fmt.Errorf("get wallet failed: %w", err)
	}

	// 為了使用悲觀鎖，我們根據 ID 重新查詢並鎖定
	lockedWallet, err := u.walletR.GetWalletByIDForUpdate(txCtx, wallet.ID)
	if err != nil {
		return nil, fmt.Errorf("lock wallet failed: %w", err)
	}

	// 5. 根據交易類型計算新餘額
	balanceBefore := lockedWallet.Balance
	var balanceAfter decimal.Decimal

	switch req.Type {
	case domain.TxTypeBet, domain.TxTypeWithdraw:
		// 扣款：檢查餘額是否足夠
		if lockedWallet.Balance.LessThan(req.Amount) {
			return nil, domain.ErrInsufficientFunds
		}
		balanceAfter = lockedWallet.Balance.Sub(req.Amount)
	case domain.TxTypeWin, domain.TxTypeDeposit, domain.TxTypeRefund:
		// 加款
		balanceAfter = lockedWallet.Balance.Add(req.Amount)
	default:
		return nil, fmt.Errorf("unknown transaction type: %s", req.Type)
	}

	// 6. 寫入流水帳紀錄
	// 將 amount 正規化：對於資料庫流水帳而言，如果是扣款可以記錄負數（視需求而定，我們這裡統一存正數，由 type 區分）
	// 我們在這裡存正數，所以不需要轉換 req.Amount
	newTx := &domain.Transaction{
		WalletID:      lockedWallet.ID,
		Type:          req.Type,
		Amount:        req.Amount,
		BalanceBefore: balanceBefore,
		BalanceAfter:  balanceAfter,
		ProviderID:    req.ProviderID,
		ProviderTxID:  req.ProviderTxID,
		ReferenceID:   req.ReferenceID,
	}

	err = u.txR.CreateTransaction(txCtx, newTx)
	if err != nil {
		// 如果發生 domain.ErrDuplicateTransaction (Unique Violation)，代表此筆交易在我們開啟 tx 之前剛好被另一個 Request 寫入了
		if err == domain.ErrDuplicateTransaction {
			// Rollback 並嘗試取得剛寫入的交易
			sqlxTx.Rollback()
			return u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
		}
		return nil, fmt.Errorf("create transaction log failed: %w", err)
	}

	// 7. 更新錢包餘額
	lockedWallet.Balance = balanceAfter
	err = u.walletR.UpdateBalance(txCtx, lockedWallet)
	if err != nil {
		return nil, fmt.Errorf("update wallet balance failed: %w", err)
	}

	// 8. 提交事務
	if err := sqlxTx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction failed: %w", err)
	}

	return newTx, nil
}
