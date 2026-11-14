package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/domain/mocks"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func setupTest(t *testing.T) (*mocks.MockWalletRepository, *mocks.MockTransactionRepository, *mocks.MockWalletCache, *mocks.MockEventPublisher, *mocks.MockUnitOfWork, domain.WalletUsecase) {
	ctrl := gomock.NewController(t)

	mockWalletRepo := mocks.NewMockWalletRepository(ctrl)
	mockTxRepo := mocks.NewMockTransactionRepository(ctrl)
	mockCache := mocks.NewMockWalletCache(ctrl)
	mockPub := mocks.NewMockEventPublisher(ctrl)
	mockUoW := mocks.NewMockUnitOfWork(ctrl)

	uc := NewWalletUsecase(mockUoW, mockWalletRepo, mockTxRepo, mockCache, mockPub)

	return mockWalletRepo, mockTxRepo, mockCache, mockPub, mockUoW, uc
}

func Test_ProcessTransaction_Success(t *testing.T) {
	mockWalletRepo, mockTxRepo, mockCache, mockPub, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Currency:     "TWD",
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
		ProviderID:   "pgsoft",
		ProviderTxID: "tx123",
	}

	// 1. 快取 PreProcess 回傳成功
	mockCache.EXPECT().PreProcess(gomock.Any(), "DEBIT", req.ProviderID, req.ProviderTxID, req.UserID, req.Currency, int64(1_000_000)).Return(domain.ResultSuccess, nil)

	// 2. UoW 執行交易閉包
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
		return fn(ctx)
	})

	wallet := &domain.Wallet{ID: 10, UserID: 1, Balance: decimal.NewFromInt(500), Currency: "TWD"}
	mockWalletRepo.EXPECT().GetWalletByUserID(gomock.Any(), int64(1), "TWD").Return(wallet, nil)
	mockWalletRepo.EXPECT().GetWalletByIDForUpdate(gomock.Any(), int64(10)).Return(wallet, nil)

	// 更新資料庫餘額
	mockTxRepo.EXPECT().CreateTransaction(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, tx *domain.Transaction) error {
		tx.ID = 999
		assert.Equal(t, "400", tx.BalanceAfter.String())
		return nil
	})
	mockWalletRepo.EXPECT().UpdateBalance(gomock.Any(), gomock.Any()).Return(nil)

	// 3. 非 Hydration 路徑不重建快取 (快取本來就存在)；若被呼叫 gomock 會直接報錯

	// 4. 發送派彩成功事件
	mockPub.EXPECT().PublishTransactionCompleted(gomock.Any(), gomock.Any()).Return(nil)

	tx, err := uc.ProcessTransaction(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, tx)
	assert.Equal(t, int64(999), tx.ID)
}

func Test_ProcessTransaction_InsufficientFunds(t *testing.T) {
	_, _, mockCache, _, _, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Currency:     "TWD",
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	// Redis 立即偵測到餘額不足
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultInsufficient, nil)

	tx, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrInsufficientFunds)
	assert.Nil(t, tx)
}

func Test_ProcessTransaction_DuplicateTx(t *testing.T) {
	_, mockTxRepo, mockCache, _, _, uc := setupTest(t)

	req := &domain.TransactionRequest{
		ProviderID:   "pgsoft",
		ProviderTxID: "tx123",
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	// Redis 阻擋重複訂單
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultDuplicateTx, nil)

	expectedTx := &domain.Transaction{ID: 888}
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pgsoft", "tx123").Return(expectedTx, nil)

	tx, err := uc.ProcessTransaction(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, expectedTx, tx)
}

func Test_ProcessTransaction_CacheMiss_RetrySuccess(t *testing.T) {
	_, _, mockCache, _, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	// 第一次嘗試：快取未擊中 (Cache Miss)
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultCacheMiss, nil)
	
	// 第一次嘗試：取得鎖失敗 (其他請求正在重建快取)
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).Return("", false, nil)

	// 第二次嘗試：重試 PreProcess 成功 (快取已被重建)
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)

	// UoW 執行失敗，僅為了讓測試提早結束
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(errors.New("db error"))

	// Redis 已預扣但 DB 失敗 → 必須刪除快取
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorContains(t, err, "db error")
}

func Test_ProcessTransaction_DBPanic_CacheInvalidated(t *testing.T) {
	_, _, mockCache, _, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)

	// 模擬 UoW 發生 Panic！
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
		panic("database dead")
	})

	// 必須刪除快取 (同步刪除成功時不需要發補償事件)
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	assert.PanicsWithValue(t, "database dead", func() {
		_, _ = uc.ProcessTransaction(context.Background(), req)
	})
}

func Test_ProcessTransaction_CacheMiss_Timeout(t *testing.T) {
	_, _, mockCache, _, _, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	// 模擬一直拿不到 Hydration Lock，迴圈 50 次都會失敗
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultCacheMiss, nil).Times(50)
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).Return("", false, nil).Times(50)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorContains(t, err, "server busy: failed to acquire hydration lock after retries")
}

func Test_ProcessTransaction_CommitFailed_CacheInvalidated(t *testing.T) {
	_, _, mockCache, _, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)

	// UoW 回傳 ErrCommitFailed，代表 DB 層面的錯誤，但不 panic
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(domain.ErrCommitFailed)

	// Commit 失敗代表結果不確定 (DB 可能其實已寫入)：不做反向加減，直接刪除快取
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorContains(t, err, "process transaction failed")
}

func Test_ProcessTransaction_InvalidAmount(t *testing.T) {
	_, _, _, _, _, uc := setupTest(t)

	// 測試金額為 0
	zeroReq := &domain.TransactionRequest{
		UserID: 1,
		Type:   domain.TxTypeBet,
		Amount: decimal.Zero,
	}
	_, err := uc.ProcessTransaction(context.Background(), zeroReq)
	assert.ErrorIs(t, err, domain.ErrInvalidAmount)

	// 測試金額為負數
	negativeReq := &domain.TransactionRequest{
		UserID: 1,
		Type:   domain.TxTypeBet,
		Amount: decimal.NewFromInt(-50),
	}
	_, err = uc.ProcessTransaction(context.Background(), negativeReq)
	assert.ErrorIs(t, err, domain.ErrInvalidAmount)
}

// P0-1：非 Commit 階段的錯誤 (例如客戶端斷線導致 ctx 取消) 也必須刪除快取
func Test_ProcessTransaction_ContextCanceledDuringDB_CacheInvalidated(t *testing.T) {
	_, _, mockCache, _, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100), ProviderID: "pgsoft", ProviderTxID: "tx1"}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(context.Canceled)

	// 清理用的 ctx 不能跟著原始請求一起被取消
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), "TWD", "pgsoft", "tx1").DoAndReturn(
		func(ctx context.Context, _ int64, _, _, _ string) error {
			assert.NoError(t, ctx.Err(), "cleanup context must not be canceled")
			return nil
		})

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, context.Canceled)
}

// P0-2：Redis 防重鎖過期後遊戲商重送 → Redis 又預扣一次，DB 唯一鍵擋下。
// 必須刪除快取 (修正 Redis 多扣)，並冪等地回傳既有交易。
func Test_ProcessTransaction_DuplicateInDBAfterRedisApplied_InvalidatesAndReturnsExisting(t *testing.T) {
	_, mockTxRepo, mockCache, _, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100), ProviderID: "pgsoft", ProviderTxID: "tx1"}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(domain.ErrDuplicateTransaction)
	mockCache.EXPECT().Invalidate(gomock.Any(), int64(1), "TWD", "pgsoft", "tx1").Return(nil)

	existing := &domain.Transaction{ID: 777}
	mockTxRepo.EXPECT().GetByProviderTxID(gomock.Any(), "pgsoft", "tx1").Return(existing, nil)

	tx, err := uc.ProcessTransaction(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, existing, tx)
}

// Redis 暫時不可用，同步刪除失敗 → 改發事件交給 Worker 重試
func Test_ProcessTransaction_InvalidateFails_FallsBackToEvent(t *testing.T) {
	_, _, mockCache, mockPub, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100), ProviderID: "pgsoft", ProviderTxID: "tx1"}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(domain.ErrCommitFailed)
	mockCache.EXPECT().Invalidate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("redis down"))
	mockPub.EXPECT().PublishCompensationEvent(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, ev *domain.CompensationEvent) error {
			assert.Equal(t, "tx1", ev.ProviderTxID)
			assert.Equal(t, int64(1), ev.UserID)
			return nil
		})

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.Error(t, err)
}

// Cache Miss 路徑沒有動過 Redis，DB 失敗時不應刪除快取 (未設定 Invalidate，被呼叫 gomock 會報錯)
func Test_ProcessTransaction_HydrationPathDBFails_NoInvalidate(t *testing.T) {
	_, _, mockCache, _, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100)}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultCacheMiss, nil)
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).Return("my-token", true, nil)
	mockCache.EXPECT().ReleaseHydrationLock(gomock.Any(), int64(1), "my-token").Return(nil)
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(domain.ErrWalletNotFound)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrWalletNotFound)
}

// Hydration Lock 持有者成功落庫後，負責用 DB 的 balanceAfter (整數單位) 重建快取
func Test_ProcessTransaction_HydrationPathSuccess_RebuildsCache(t *testing.T) {
	mockWalletRepo, mockTxRepo, mockCache, mockPub, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Currency: "TWD", Type: domain.TxTypeBet, Amount: decimal.RequireFromString("100.5"), ProviderID: "pgsoft", ProviderTxID: "tx1"}

	mockCache.EXPECT().PreProcess(gomock.Any(), "DEBIT", "pgsoft", "tx1", int64(1), "TWD", int64(1_005_000)).Return(domain.ResultCacheMiss, nil)
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).Return("my-token", true, nil)
	mockCache.EXPECT().ReleaseHydrationLock(gomock.Any(), int64(1), "my-token").Return(nil)

	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
		return fn(ctx)
	})
	wallet := &domain.Wallet{ID: 10, UserID: 1, Balance: decimal.NewFromInt(500), Currency: "TWD"}
	mockWalletRepo.EXPECT().GetWalletByUserID(gomock.Any(), int64(1), "TWD").Return(wallet, nil)
	mockWalletRepo.EXPECT().GetWalletByIDForUpdate(gomock.Any(), int64(10)).Return(wallet, nil)
	mockTxRepo.EXPECT().CreateTransaction(gomock.Any(), gomock.Any()).Return(nil)
	mockWalletRepo.EXPECT().UpdateBalance(gomock.Any(), gomock.Any()).Return(nil)

	// 500 - 100.5 = 399.5 → 3,995,000 最小單位
	mockCache.EXPECT().InitCacheIfMissing(gomock.Any(), int64(1), "TWD", int64(3_995_000)).Return(nil)
	mockPub.EXPECT().PublishTransactionCompleted(gomock.Any(), gomock.Any()).Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.NoError(t, err)
}

// 等待 Hydration 期間客戶端斷線，應立刻放棄而不是空等 2.5 秒
func Test_ProcessTransaction_CacheMissWait_RespectsContextCancel(t *testing.T) {
	_, _, mockCache, _, _, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100)}

	ctx, cancel := context.WithCancel(context.Background())
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultCacheMiss, nil)
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).DoAndReturn(func(context.Context, int64) (string, bool, error) {
		cancel() // 模擬客戶端在等待前斷線
		return "", false, nil
	})

	_, err := uc.ProcessTransaction(ctx, req)
	require.ErrorIs(t, err, context.Canceled)
}

// 小數超過 4 位會讓 Redis 與 DB 算出不同結果，必須拒絕
func Test_ProcessTransaction_TooManyDecimals_Rejected(t *testing.T) {
	_, _, _, _, _, uc := setupTest(t)

	req := &domain.TransactionRequest{UserID: 1, Type: domain.TxTypeBet, Amount: decimal.RequireFromString("0.00001")}
	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorIs(t, err, domain.ErrInvalidAmount)
}
