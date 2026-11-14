package usecase

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

const (
	// hydrationMaxRetries 等待別人重建快取的最大重試次數 (約 2.5 秒)
	hydrationMaxRetries = 50
	// hydrationRetryBase 每次重試的基本等待時間
	hydrationRetryBase = 50 * time.Millisecond
	// hydrationRetryJitter 隨機抖動上限，避免驚群效應
	hydrationRetryJitter = 20 * time.Millisecond
	// cleanupTimeout 失敗後清理快取/發事件的時間上限
	cleanupTimeout = 2 * time.Second
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
	// 1. 基本防呆檢查 (Domain Invariant Guard: 核心業務規則斷言，金額必須大於零)
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, domain.ErrInvalidAmount
	}
	// 轉為整數最小單位，Redis 端全程用整數運算，避免浮點誤差
	amountUnits, err := domain.ToMinorUnits(req.Amount)
	if err != nil {
		return nil, err
	}

	// 2. Redis Fast-path：統一處理所有交易類型的防重鎖與餘額預處理
	var redisOp string
	switch req.Type {
	case domain.TxTypeBet, domain.TxTypeWithdraw:
		redisOp = domain.OpDebit
	case domain.TxTypeWin, domain.TxTypeDeposit, domain.TxTypeRefund:
		redisOp = domain.OpCredit
	default:
		return nil, fmt.Errorf("unknown transaction type: %s", req.Type)
	}

	var result int
	var hydrationLocked bool

	for range hydrationMaxRetries {
		result, err = u.cache.PreProcess(ctx, redisOp, req.ProviderID, req.ProviderTxID, req.UserID, req.Currency, amountUnits)
		if err != nil {
			return nil, fmt.Errorf("redis pre-process failed: %w", err)
		}

		if result != domain.ResultCacheMiss {
			break
		}

		// 遇到 Cache Miss，嘗試爭搶 Hydration Lock
		hydrationLocked, err = u.cache.AcquireHydrationLock(ctx, req.UserID)
		if err != nil {
			return nil, fmt.Errorf("acquire hydration lock failed: %w", err)
		}

		if hydrationLocked {
			// 搶到鎖！由我們負責查 DB 並重建快取。
			// WithoutCancel：即使客戶端已斷線，也要確實釋放鎖 (同時保留 trace_id 等 context 值)
			defer u.cache.ReleaseHydrationLock(context.WithoutCancel(ctx), req.UserID)
			break
		}

		// 沒搶到鎖，代表別人正在重建快取。等待一下再試；客戶端斷線就立刻放棄，不要空等。
		if err := sleepWithJitter(ctx); err != nil {
			return nil, err
		}
	}

	if result == domain.ResultCacheMiss && !hydrationLocked {
		return nil, fmt.Errorf("server busy: failed to acquire hydration lock after retries")
	}

	switch result {
	case domain.ResultDuplicateTx:
		// 鎖已存在，代表這筆交易已處理過或正在處理，直接從 DB 查出歷史資料回傳
		existingTx, getErr := u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
		if getErr != nil || existingTx == nil {
			return nil, fmt.Errorf("concurrent request blocked by redis lock")
		}
		return existingTx, nil
	case domain.ResultInsufficient:
		return nil, domain.ErrInsufficientFunds
	}

	// redisApplied 代表 Redis 已經「先」改了餘額 (DEBIT 預扣 / CREDIT 預加)。
	// 從這裡開始，只要 DB 沒有「確定成功」，Redis 就可能跟 DB 不一致，必須刪除快取。
	// Cache Miss 路徑 (hydrationLocked) 沒有動過 Redis，失敗時不需要清理。
	redisApplied := result == domain.ResultSuccess

	// 攔截 DB 階段的 Panic：DB 交易會回滾，但 Redis 已被修改，必須刪除快取後再把 Panic 往外丟
	defer func() {
		if r := recover(); r != nil {
			if redisApplied {
				u.invalidateCache(ctx, req)
			}
			panic(r) // 交給 Gin Recovery 回傳 500
		}
	}()

	newTx, wallet, err := u.persistTransaction(ctx, req)
	if err != nil {
		// 任何 DB 失敗都一樣處理：包含 Commit 失敗 (結果不確定)、ctx 取消、
		// DB 餘額不足 (代表 Redis 已經漂移)、唯一鍵衝突 (Redis 防重鎖過期後的重送) 等。
		if redisApplied {
			u.invalidateCache(ctx, req)
		}

		// 唯一鍵衝突：這筆交易早就成功過了，回傳既有交易 (冪等)
		if errors.Is(err, domain.ErrDuplicateTransaction) {
			existingTx, getErr := u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
			if getErr != nil || existingTx == nil {
				return nil, fmt.Errorf("create transaction failed (duplicate), but fallback query failed: %w", getErr)
			}
			return existingTx, nil
		}

		return nil, fmt.Errorf("process transaction failed: %w", err)
	}

	// 只有 Hydration Lock 持有者負責重建快取。
	// 其他請求的快取本來就存在 (HSETNX 也不會寫入)；若快取在途中被刪除，
	// 交給下一個 Cache Miss 的請求重建，避免舊的 balanceAfter 搶先寫入造成快取過期資料。
	if hydrationLocked {
		if balanceUnits, convErr := domain.ToMinorUnits(newTx.BalanceAfter); convErr == nil {
			_ = u.cache.InitCacheIfMissing(ctx, req.UserID, req.Currency, balanceUnits)
		}
	}

	// 發送非同步事件，交給 Worker 處理後續任務 (例如報表、返水)
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
	_ = u.pub.PublishTransactionCompleted(context.WithoutCancel(ctx), event)

	return newTx, nil
}

// persistTransaction 在單一 DB 交易內完成：鎖定錢包、計算新餘額、寫入流水帳、更新餘額。
func (u *walletUsecase) persistTransaction(ctx context.Context, req *domain.TransactionRequest) (*domain.Transaction, *domain.Wallet, error) {
	var newTx *domain.Transaction
	var wallet *domain.Wallet

	err := u.uow.DoTx(ctx, func(txCtx context.Context) error {
		// 取得錢包並加上悲觀鎖 (SELECT ... FOR UPDATE)
		var innerErr error
		wallet, innerErr = u.walletR.GetWalletByUserID(txCtx, req.UserID, req.Currency)
		if innerErr != nil {
			return fmt.Errorf("get wallet failed: %w", innerErr)
		}

		lockedWallet, innerErr := u.walletR.GetWalletByIDForUpdate(txCtx, wallet.ID)
		if innerErr != nil {
			return fmt.Errorf("lock wallet failed: %w", innerErr)
		}

		// 根據交易類型計算新餘額
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

		// 寫入流水帳紀錄
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

		// 更新錢包餘額
		lockedWallet.Balance = balanceAfter
		if innerErr = u.walletR.UpdateBalance(txCtx, lockedWallet); innerErr != nil {
			return fmt.Errorf("update wallet balance failed: %w", innerErr)
		}

		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return newTx, wallet, nil
}

// invalidateCache 在 DB 失敗或結果不確定時刪除 Redis 快取，讓下一個請求從 DB 重建。
//
// 為什麼不做「反向加減」補償？因為 Commit 失敗時，DB 可能其實已經寫入 (只是回應丟失)，
// 反向加減會把「少扣」變成「多給」。刪除快取不需要知道真相，只要知道「不確定」就能正確處理。
//
// 同步刪除失敗 (Redis 暫時不可用) 時，改發事件交給 Worker 重試。
func (u *walletUsecase) invalidateCache(ctx context.Context, req *domain.TransactionRequest) {
	// 原始請求可能已被取消 (這正是常見的失敗原因)，清理工作要用獨立的 context
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	log := logger.WithTraceID(ctx).With(
		zap.Int64("user_id", req.UserID),
		zap.String("currency", req.Currency),
		zap.String("provider_id", req.ProviderID),
		zap.String("provider_tx_id", req.ProviderTxID),
	)

	err := u.cache.Invalidate(cleanupCtx, req.UserID, req.Currency, req.ProviderID, req.ProviderTxID)
	if err == nil {
		log.Warn("wallet cache invalidated after DB failure")
		return
	}

	log.Error("sync cache invalidation failed, falling back to async worker", zap.Error(err))
	pubErr := u.pub.PublishCompensationEvent(cleanupCtx, &domain.CompensationEvent{
		ProviderID:   req.ProviderID,
		ProviderTxID: req.ProviderTxID,
		UserID:       req.UserID,
		Currency:     req.Currency,
		Type:         req.Type,
		Amount:       req.Amount,
		Timestamp:    time.Now(),
	})
	if pubErr != nil {
		// 兩條路都失敗：快取可能過期到 TTL 為止。DB 仍是最後防線 (會擋下超扣)，
		// 但這條日誌應該接上告警，讓人工介入。
		log.Error("CRITICAL: cache invalidation event publish failed, wallet cache may be stale", zap.Error(pubErr))
	}
}

// sleepWithJitter 等待「基本時間 + 隨機抖動」，若 ctx 先被取消則提早回傳錯誤。
func sleepWithJitter(ctx context.Context) error {
	d := hydrationRetryBase + rand.N(hydrationRetryJitter)
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
