package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/repository/postgres"
	"github.com/bosstest/nexus-core/internal/repository/redis"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type walletUsecase struct {
	db      *sqlx.DB
	walletR domain.WalletRepository
	txR     domain.TransactionRepository
	cache   *redis.WalletCache
	pub     domain.EventPublisher
}

// NewWalletUsecase 建立 WalletUsecase 實例
func NewWalletUsecase(
	db *sqlx.DB,
	walletR domain.WalletRepository,
	txR domain.TransactionRepository,
	cache *redis.WalletCache,
	pub domain.EventPublisher,
) domain.WalletUsecase {
	return &walletUsecase{
		db:      db,
		walletR: walletR,
		txR:     txR,
		cache:   cache,
		pub:     pub,
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

	// 2. Redis Fast-path：統一處理所有交易類型的防重鎖與餘額預處理
	// DEBIT：原子驗證餘額並預扣，Cache Miss 時 fallthrough 到 DB
	// CREDIT：原子加款，防止重複派彩，Cache Miss 時 fallthrough 到 DB
	var redisOp string
	switch req.Type {
	case domain.TxTypeBet, domain.TxTypeWithdraw:
		redisOp = redis.OpDebit
	case domain.TxTypeWin, domain.TxTypeDeposit, domain.TxTypeRefund:
		redisOp = redis.OpCredit
	default:
		return nil, fmt.Errorf("unknown transaction type: %s", req.Type)
	}

	amountFloat, _ := req.Amount.Float64()
	result, err := u.cache.PreProcess(ctx, redisOp, req.ProviderID, req.ProviderTxID, req.UserID, req.Currency, amountFloat)
	if err != nil {
		// 如果 Redis 掛了，可以選擇 Fallback 降級回純 DB 模式，這邊為了展示先報錯
		return nil, fmt.Errorf("redis pre-process failed: %w", err)
	}

	switch result {
	case redis.ResultDuplicateTx:
		// 鎖已存在，代表這筆交易已處理過或正在處理，直接從 DB 查出歷史資料回傳
		existingTx, err := u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
		if err != nil || existingTx == nil {
			return nil, fmt.Errorf("concurrent request blocked by redis lock")
		}
		return existingTx, nil
	case redis.ResultInsufficient:
		return nil, domain.ErrInsufficientFunds
	case redis.ResultCacheMiss:
		// 快取沒有餘額資料，繼續往下走打 DB，落庫成功後 InitCacheIfMissing 補種
	case redis.ResultSuccess:
		// 預處理成功（DEBIT 已預扣 / CREDIT 已加款），繼續往下完成 DB 最終落庫
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
		// 走到這裡通常是很嚴重的系統異常，此時 Redis 已經預處理了，DB 卻沒寫入。
		// 透過 RabbitMQ 發送補償事件，交由 Worker 去執行 Redis 逆向補償並移除防重鎖，確保最終一致性。
		_ = u.pub.PublishCompensationEvent(context.Background(), &domain.CompensationEvent{
			ProviderID:   req.ProviderID,
			ProviderTxID: req.ProviderTxID,
			UserID:       req.UserID,
			Currency:     req.Currency,
			Type:         req.Type,
			Amount:       req.Amount,
			Timestamp:    time.Now(),
		})
		return nil, fmt.Errorf("commit transaction failed: %w", err)
	}

	// 成功落庫後，嘗試將餘額寫入 Redis 作為初始值。
	// 使用 HSETNX (若不存在才寫入)，避免覆寫其他並發交易的 HINCRBYFLOAT 增減，確保快取數據正確。
	balanceAfterFloat, _ := newTx.BalanceAfter.Float64()
	_ = u.cache.InitCacheIfMissing(ctx, req.UserID, req.Currency, balanceAfterFloat)

	// 9. 發送非同步事件，交給 Worker 處理後續任務 (例如報表、返水)
	// 即使發送失敗也不 rollback，因為核心金流已成功，可透過對帳腳本補償
	event := &domain.TransactionCompletedEvent{
		TransactionID: fmt.Sprintf("%d", newTx.ID),
		ProviderID:    newTx.ProviderID,
		ProviderTxID:  newTx.ProviderTxID,
		UserID:        wallet.UserID,
		Currency:      wallet.Currency,
		Type:          newTx.Type,
		Amount:        newTx.Amount,
		BalanceAfter:  newTx.BalanceAfter,
		Timestamp:     newTx.CreatedAt,
	}
	_ = u.pub.PublishTransactionCompleted(context.Background(), event)

	return newTx, nil
}
