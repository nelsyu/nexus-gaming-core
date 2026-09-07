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
	mockCache.EXPECT().PreProcess(gomock.Any(), "DEBIT", req.ProviderID, req.ProviderTxID, req.UserID, req.Currency, 100.0).Return(domain.ResultSuccess, nil)

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

	// 3. 初始化快取
	mockCache.EXPECT().InitCacheIfMissing(gomock.Any(), req.UserID, req.Currency, 400.0).Return(nil)

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
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).Return(false, nil)

	// 第二次嘗試：重試 PreProcess 成功 (快取已被重建)
	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)

	// UoW 執行失敗，僅為了讓測試提早結束
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(errors.New("db error"))

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorContains(t, err, "db error")
}

func Test_ProcessTransaction_DBPanic_CompensationPublished(t *testing.T) {
	_, _, mockCache, mockPub, mockUoW, uc := setupTest(t)

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

	// 必須呼叫 Publisher 發送補償事件
	mockPub.EXPECT().PublishCompensationEvent(gomock.Any(), gomock.Any()).Return(nil)

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
	mockCache.EXPECT().AcquireHydrationLock(gomock.Any(), int64(1)).Return(false, nil).Times(50)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorContains(t, err, "server busy: failed to acquire hydration lock after retries")
}

func Test_ProcessTransaction_DBError_CompensationPublished(t *testing.T) {
	_, _, mockCache, mockPub, mockUoW, uc := setupTest(t)

	req := &domain.TransactionRequest{
		UserID:       1,
		Type:         domain.TxTypeBet,
		Amount:       decimal.NewFromInt(100),
	}

	mockCache.EXPECT().PreProcess(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.ResultSuccess, nil)

	// UoW 回傳 ErrCommitFailed，代表 DB 層面的錯誤，但不 panic
	mockUoW.EXPECT().DoTx(gomock.Any(), gomock.Any()).Return(domain.ErrCommitFailed)

	// 必須觸發發送補償事件
	mockPub.EXPECT().PublishCompensationEvent(gomock.Any(), gomock.Any()).Return(nil)

	_, err := uc.ProcessTransaction(context.Background(), req)
	require.ErrorContains(t, err, "process transaction failed")
}
