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
	if err := validateRelations(req); err != nil {
		return nil, err
	}

	// 退款走獨立流程：金額以 DB 中的原注單為準，不能先用請求金額在 Redis 預加款
	if req.Type == domain.TxTypeRefund {
		return u.processRefund(ctx, req)
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
	case domain.TxTypeWin, domain.TxTypeDeposit:
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
		var lockToken string
		lockToken, hydrationLocked, err = u.cache.AcquireHydrationLock(ctx, req.UserID)
		if err != nil {
			return nil, fmt.Errorf("acquire hydration lock failed: %w", err)
		}

		if hydrationLocked {
			// 搶到鎖！由我們負責查 DB 並重建快取。
			// 釋放時帶上 token：若我們處理太久、鎖已過期並被別人取得，就不會誤刪別人的鎖。
			// WithoutCancel：即使客戶端已斷線，也要確實釋放鎖 (同時保留 trace_id 等 context 值)
			defer u.cache.ReleaseHydrationLock(context.WithoutCancel(ctx), req.UserID, lockToken)
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
	u.publishCompleted(ctx, newTx, wallet)

	return newTx, nil
}

// persistTransaction 在單一 DB 交易內完成：鎖定錢包、計算新餘額、寫入流水帳、更新餘額。
func (u *walletUsecase) persistTransaction(ctx context.Context, req *domain.TransactionRequest) (*domain.Transaction, *domain.Wallet, error) {
	var newTx *domain.Transaction
	var wallet *domain.Wallet

	err := u.uow.DoTx(ctx, func(txCtx context.Context) error {
		lockedWallet, innerErr := u.lockWallet(txCtx, req.UserID, req.Currency)
		if innerErr != nil {
			return innerErr
		}
		wallet = lockedWallet

		// 注單規則檢查：必須在鎖住錢包之後做，同一錢包的 BET / WIN / REFUND 才會依序執行，不會互相穿插
		if innerErr = u.checkRoundRules(txCtx, lockedWallet.ID, req); innerErr != nil {
			return innerErr
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
		case domain.TxTypeWin, domain.TxTypeDeposit:
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
			RoundID:       req.RoundID,
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

// lockWallet 取得錢包並加上悲觀鎖 (SELECT ... FOR UPDATE)。必須在 DB 交易內呼叫。
func (u *walletUsecase) lockWallet(txCtx context.Context, userID int64, currency string) (*domain.Wallet, error) {
	wallet, err := u.walletR.GetWalletByUserID(txCtx, userID, currency)
	if err != nil {
		return nil, fmt.Errorf("get wallet failed: %w", err)
	}
	locked, err := u.walletR.GetWalletByIDForUpdate(txCtx, wallet.ID)
	if err != nil {
		return nil, fmt.Errorf("lock wallet failed: %w", err)
	}
	return locked, nil
}

// validateRelations 檢查交易關聯欄位是否齊全 (不需查 DB 的規則)
func validateRelations(req *domain.TransactionRequest) error {
	switch req.Type {
	case domain.TxTypeBet, domain.TxTypeWin:
		if req.RoundID == "" {
			return domain.ErrRoundIDRequired
		}
	case domain.TxTypeRefund:
		if req.RelatedProviderTxID == "" {
			return domain.ErrRefundTargetRequired
		}
	}
	return nil
}

// checkRoundRules 檢查需要查 DB 的注單規則
func (u *walletUsecase) checkRoundRules(txCtx context.Context, walletID int64, req *domain.TransactionRequest) error {
	switch req.Type {
	case domain.TxTypeBet:
		// 退款比下注先到時，這筆 BET 已被作廢：拒絕扣款，否則玩家會「先拿到退款、後被扣款」的順序錯亂
		voided, err := u.txR.IsBetVoided(txCtx, req.ProviderID, req.ProviderTxID)
		if err != nil {
			return fmt.Errorf("check voided bet failed: %w", err)
		}
		if voided {
			return domain.ErrBetVoided
		}
	case domain.TxTypeWin:
		// 同一局必須有尚未退款的 BET，防止「沒下注就派彩」
		ok, err := u.txR.HasActiveBetInRound(txCtx, walletID, req.ProviderID, req.RoundID)
		if err != nil {
			return fmt.Errorf("check round bet failed: %w", err)
		}
		if !ok {
			return domain.ErrWinWithoutBet
		}
	}
	return nil
}

// processRefund 處理退款 (Rollback)：
//
//	原注單存在         → 退回「原注單金額」(不採信請求中的金額)
//	原注單已退過       → 回傳第一次的退款結果 (冪等，不重複加錢)
//	原注單已派彩       → 拒絕 (不能同時拿派彩和退款)
//	原注單還沒到       → 記錄作廢 (tombstone)，之後該 BET 到達時會被拒絕
//
// 退款不走 Redis 預處理，直接落庫後刪除快取，讓下一個請求從 DB 重建。
func (u *walletUsecase) processRefund(ctx context.Context, req *domain.TransactionRequest) (*domain.Transaction, error) {
	var refund *domain.Transaction
	var wallet *domain.Wallet
	var voided bool

	err := u.uow.DoTx(ctx, func(txCtx context.Context) error {
		lockedWallet, err := u.lockWallet(txCtx, req.UserID, req.Currency)
		if err != nil {
			return err
		}
		wallet = lockedWallet

		bet, err := u.txR.GetByProviderTxID(txCtx, req.ProviderID, req.RelatedProviderTxID)
		if err != nil {
			return fmt.Errorf("get original bet failed: %w", err)
		}
		if bet == nil {
			// 退款比下注先到：記下作廢紀錄並 commit (回傳 nil)，交易結束後再回報「找不到原注單」
			if err := u.txR.VoidBet(txCtx, lockedWallet.ID, req.ProviderID, req.RelatedProviderTxID, req.ProviderTxID); err != nil {
				return fmt.Errorf("void bet failed: %w", err)
			}
			voided = true
			return nil
		}
		if bet.Type != domain.TxTypeBet || bet.WalletID != lockedWallet.ID {
			return domain.ErrInvalidRefundTarget
		}

		settled, err := u.txR.HasWinInRound(txCtx, lockedWallet.ID, bet.ProviderID, bet.RoundID)
		if err != nil {
			return fmt.Errorf("check round settlement failed: %w", err)
		}
		if settled {
			return domain.ErrBetAlreadySettled
		}

		if !req.Amount.Equal(bet.Amount) {
			logger.WithTraceID(ctx).Warn("Refund amount differs from original bet, using original bet amount",
				zap.String("provider_tx_id", req.ProviderTxID),
				zap.String("requested", req.Amount.String()),
				zap.String("original", bet.Amount.String()),
			)
		}

		balanceAfter := lockedWallet.Balance.Add(bet.Amount)
		refund = &domain.Transaction{
			WalletID:            lockedWallet.ID,
			Type:                domain.TxTypeRefund,
			Amount:              bet.Amount, // 以原注單金額為準
			BalanceBefore:       lockedWallet.Balance,
			BalanceAfter:        balanceAfter,
			ProviderID:          req.ProviderID,
			ProviderTxID:        req.ProviderTxID,
			ReferenceID:         req.ReferenceID,
			RoundID:             bet.RoundID,
			RelatedProviderTxID: bet.ProviderTxID,
		}
		if err := u.txR.CreateTransaction(txCtx, refund); err != nil {
			return err // 可能是 ErrDuplicateTransaction 或 ErrBetAlreadyRefunded
		}

		lockedWallet.Balance = balanceAfter
		if err := u.walletR.UpdateBalance(txCtx, lockedWallet); err != nil {
			return fmt.Errorf("update wallet balance failed: %w", err)
		}
		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, domain.ErrDuplicateTransaction):
			// 同一個退款請求重送：回傳第一次的結果
			return u.existingTransaction(ctx, func(ctx context.Context) (*domain.Transaction, error) {
				return u.txR.GetByProviderTxID(ctx, req.ProviderID, req.ProviderTxID)
			})
		case errors.Is(err, domain.ErrBetAlreadyRefunded):
			// 同一筆 BET 已被另一個退款請求退過：回傳那一次的退款，不重複加錢
			return u.existingTransaction(ctx, func(ctx context.Context) (*domain.Transaction, error) {
				return u.txR.GetRefundByTarget(ctx, req.ProviderID, req.RelatedProviderTxID)
			})
		}
		return nil, fmt.Errorf("process refund failed: %w", err)
	}
	if voided {
		return nil, domain.ErrRefundTargetNotFound
	}

	// DB 餘額已改變，刪除快取讓下一個請求從 DB 重建
	u.invalidateCache(ctx, req)
	u.publishCompleted(ctx, refund, wallet)
	return refund, nil
}

// existingTransaction 冪等回應：查出既有交易回傳，查不到視為錯誤
func (u *walletUsecase) existingTransaction(ctx context.Context, find func(context.Context) (*domain.Transaction, error)) (*domain.Transaction, error) {
	tx, err := find(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch existing transaction failed: %w", err)
	}
	if tx == nil {
		return nil, errors.New("conflicting transaction reported but not found")
	}
	return tx, nil
}

// publishCompleted 發送交易完成事件。失敗不影響交易結果 (核心金流已落庫)，可由對帳補償。
func (u *walletUsecase) publishCompleted(ctx context.Context, tx *domain.Transaction, wallet *domain.Wallet) {
	event := &domain.TransactionCompletedEvent{
		TransactionID: fmt.Sprintf("%d", tx.ID),
		ProviderID:    tx.ProviderID,
		ProviderTxID:  tx.ProviderTxID,
		UserID:        wallet.UserID,
		Currency:      wallet.Currency,
		Type:          tx.Type,
		Amount:        tx.Amount,
		BalanceAfter:  tx.BalanceAfter,
		Timestamp:     tx.CreatedAt,
	}
	_ = u.pub.PublishTransactionCompleted(context.WithoutCancel(ctx), event)
}

// invalidateCache 刪除 Redis 快取，讓下一個請求從 DB 重建。
// 用於 DB 失敗或結果不確定時，以及不經 Redis 預處理就直接改了 DB 餘額時 (例如退款)。
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
		log.Info("wallet cache invalidated")
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
