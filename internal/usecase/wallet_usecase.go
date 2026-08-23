package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/shopspring/decimal"
)

type walletUsecase struct {
	uow     domain.UnitOfWork
	walletR domain.WalletRepository
	txR     domain.TransactionRepository
	cache   domain.WalletCache
	pub     domain.EventPublisher
}

// NewWalletUsecase 建立 WalletUsecase 實例
func NewWalletUsecase(
	uow domain.UnitOfWork,
	walletR domain.WalletRepository,
	txR domain.TransactionRepository,
	cache domain.WalletCache,
	pub domain.EventPublisher,
) domain.WalletUsecase {
	return &walletUsecase{
		uow:     uow,
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
		redisOp = domain.OpDebit
	case domain.TxTypeWin, domain.TxTypeDeposit, domain.TxTypeRefund:
		redisOp = domain.OpCredit
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
	case domain.ResultDuplicateTx:
		// 鎖已存在，代表這筆交易已處理過或正在處理，直接從 DB 查出歷史資料回傳
		existingTx, err := u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
		if err != nil || existingTx == nil {
			return nil, fmt.Errorf("concurrent request blocked by redis lock")
		}
		return existingTx, nil
	case domain.ResultInsufficient:
		return nil, domain.ErrInsufficientFunds
	case domain.ResultCacheMiss:
		// 快取沒有餘額資料，繼續往下走打 DB，落庫成功後 InitCacheIfMissing 補種
	case domain.ResultSuccess:
		// 預處理成功（DEBIT 已預扣 / CREDIT 已加款），繼續往下完成 DB 最終落庫
	}

	var newTx *domain.Transaction
	var wallet *domain.Wallet

	// 3. 開啟資料庫事務 (DB Transaction)，透過 UnitOfWork 封裝
	err = u.uow.DoTx(ctx, func(txCtx context.Context) error {
		// 4. 取得錢包並加上悲觀鎖 (SELECT ... FOR UPDATE)
		var innerErr error
		wallet, innerErr = u.walletR.GetWalletByUserID(txCtx, req.UserID, req.Currency)
		if innerErr != nil {
			return fmt.Errorf("get wallet failed: %w", innerErr)
		}

		// 為了使用悲觀鎖，我們根據 ID 重新查詢並鎖定
		lockedWallet, innerErr := u.walletR.GetWalletByIDForUpdate(txCtx, wallet.ID)
		if innerErr != nil {
			return fmt.Errorf("lock wallet failed: %w", innerErr)
		}

		// 5. 根據交易類型計算新餘額
		balanceBefore := lockedWallet.Balance
		var balanceAfter decimal.Decimal

		switch req.Type {
		case domain.TxTypeBet, domain.TxTypeWithdraw:
			if lockedWallet.Balance.LessThan(req.Amount) {
				return domain.ErrInsufficientFunds
			}
			balanceAfter = lockedWallet.Balance.Sub(req.Amount)
		case domain.TxTypeWin, domain.TxTypeDeposit, domain.TxTypeRefund:
			balanceAfter = lockedWallet.Balance.Add(req.Amount)
		default:
			return fmt.Errorf("unknown transaction type: %s", req.Type)
		}

		// 6. 寫入流水帳紀錄
		newTx = &domain.Transaction{
			WalletID:      lockedWallet.ID,
			Type:          req.Type,
			Amount:        req.Amount,
			BalanceBefore: balanceBefore,
			BalanceAfter:  balanceAfter,
			ProviderID:    req.ProviderID,
			ProviderTxID:  req.ProviderTxID,
			ReferenceID:   req.ReferenceID,
		}

		if innerErr = u.txR.CreateTransaction(txCtx, newTx); innerErr != nil {
			return innerErr // 可能會是 domain.ErrDuplicateTransaction
		}

		// 7. 更新錢包餘額
		lockedWallet.Balance = balanceAfter
		if innerErr = u.walletR.UpdateBalance(txCtx, lockedWallet); innerErr != nil {
			return fmt.Errorf("update wallet balance failed: %w", innerErr)
		}

		return nil
	})

	// 錯誤處理區塊：依照 err 型別判斷
	if err != nil {
		// 若是唯一鍵衝突，直接查出既有交易回傳
		if errors.Is(err, domain.ErrDuplicateTransaction) {
			existingTx, getErr := u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
			if getErr != nil || existingTx == nil {
				return nil, fmt.Errorf("create transaction failed (duplicate), but fallback query failed: %w", getErr)
			}
			return existingTx, nil
		}

		// 若是 Commit 階段才失敗，發送 RabbitMQ 補償事件
		if errors.Is(err, domain.ErrCommitFailed) {
			_ = u.pub.PublishCompensationEvent(context.Background(), &domain.CompensationEvent{
				ProviderID:   req.ProviderID,
				ProviderTxID: req.ProviderTxID,
				UserID:       req.UserID,
				Currency:     req.Currency,
				Type:         req.Type,
				Amount:       req.Amount,
				Timestamp:    time.Now(),
			})
		}
		
		return nil, fmt.Errorf("process transaction failed: %w", err)
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
