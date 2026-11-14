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

func setupReconcile(t *testing.T) (*mocks.MockReconcileRepository, *mocks.MockCacheInspector, *ReconcileUsecase) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockReconcileRepository(ctrl)
	cache := mocks.NewMockCacheInspector(ctrl)
	return repo, cache, NewReconcileUsecase(repo, cache)
}

func wallet(id int64, balance string, version int) domain.WalletSnapshot {
	return domain.WalletSnapshot{ID: id, UserID: id * 10, Currency: "TWD", Balance: decimal.RequireFromString(balance), Version: version}
}

// RecheckDelay = 0：測試不需要真的等待
var testOpts = ReconcileOptions{BatchSize: 2, IssueLimit: 100, RecheckDelay: 0}

func TestReconcile_AllConsistent(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	w1, w2 := wallet(1, "100", 1), wallet(2, "50.5", 3)
	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{w1, w2}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 1_000_000, 2: 505_000}, nil)
	// 第一批剛好滿 2 筆，要再查下一頁才知道結束
	repo.EXPECT().ListWallets(gomock.Any(), int64(2), 2).Return(nil, nil)

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	assert.False(t, report.HasIssues())
	assert.Equal(t, 2, report.WalletsScanned)
	assert.Equal(t, 2, report.CacheCompared)
}

func TestReconcile_LedgerIssuesReported(t *testing.T) {
	repo, _, uc := setupReconcile(t)

	issues := []domain.LedgerIssue{{Check: domain.CheckChainBreak, WalletID: 1, TransactionID: 7}}
	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(issues, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return(nil, nil)

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	assert.True(t, report.HasIssues())
	assert.Equal(t, issues, report.LedgerIssues)
}

// 沒有快取的錢包不比對 (不算問題)
func TestReconcile_WalletWithoutCache_NotCompared(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{wallet(1, "100", 1)}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{}, nil)

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	assert.False(t, report.HasIssues())
	assert.Equal(t, 0, report.CacheCompared)
}

// 核心案例：複查後 version 沒變、仍不一致 → 確定漂移；預設只回報不修復
func TestReconcile_PersistentDrift_ReportedNotHealedByDefault(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	w := wallet(1, "100", 5)
	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 0}, nil) // 快取說 0 元
	repo.EXPECT().GetWalletsByIDs(gomock.Any(), []int64{1}).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 0}, nil)
	// 未設定 InvalidateWallet：預設模式下若被呼叫，gomock 會讓測試失敗

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	require.Len(t, report.CacheIssues, 1)
	assert.Equal(t, domain.CacheIssue{WalletID: 1, UserID: 10, Currency: "TWD", DBUnits: 1_000_000, CacheUnits: 0}, report.CacheIssues[0])
	assert.Equal(t, 0, report.CacheHealed)
}

func TestReconcile_PersistentDrift_HealedWhenEnabled(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	w := wallet(1, "100", 5)
	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 0}, nil).Times(2)
	repo.EXPECT().GetWalletsByIDs(gomock.Any(), []int64{1}).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().InvalidateWallet(gomock.Any(), int64(10), "TWD").Return(nil)

	opts := testOpts
	opts.HealCache = true
	report, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, report.CacheIssues, 1)
	assert.True(t, report.CacheIssues[0].Healed)
	assert.Equal(t, 1, report.CacheHealed)
}

// 修復失敗時仍要回報問題，只是標記為未修復，不讓整次對帳失敗
func TestReconcile_HealFails_StillReported(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	w := wallet(1, "100", 5)
	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 0}, nil).Times(2)
	repo.EXPECT().GetWalletsByIDs(gomock.Any(), []int64{1}).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().InvalidateWallet(gomock.Any(), int64(10), "TWD").Return(errors.New("redis down"))

	opts := testOpts
	opts.HealCache = true
	report, err := uc.Run(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, report.CacheIssues, 1)
	assert.False(t, report.CacheIssues[0].Healed)
}

// 複查期間 DB 有新交易 (version 改變)：無法判定，跳過而不是誤報
func TestReconcile_WalletBusyDuringRecheck_Skipped(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{wallet(1, "100", 5)}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 900_000}, nil)
	repo.EXPECT().GetWalletsByIDs(gomock.Any(), []int64{1}).Return([]domain.WalletSnapshot{wallet(1, "90", 6)}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 800_000}, nil)

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	assert.False(t, report.HasIssues())
	assert.Equal(t, 1, report.CacheSuspects)
	assert.Equal(t, 1, report.CacheSkippedBusy)
}

// 進行中的交易：第一次看 Redis 已預扣、DB 還沒寫；複查時已一致 → 不是問題
func TestReconcile_InFlightTransaction_ResolvedOnRecheck(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	w := wallet(1, "100", 5)
	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 900_000}, nil)
	repo.EXPECT().GetWalletsByIDs(gomock.Any(), []int64{1}).Return([]domain.WalletSnapshot{w}, nil)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{1: 1_000_000}, nil)

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	assert.False(t, report.HasIssues())
}

// 分頁：第二頁從第一頁最後一個 id 之後開始，資料不足一頁時停止
func TestReconcile_PaginatesByLastID(t *testing.T) {
	repo, cache, uc := setupReconcile(t)

	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, nil)
	gomock.InOrder(
		repo.EXPECT().ListWallets(gomock.Any(), int64(0), 2).Return([]domain.WalletSnapshot{wallet(3, "1", 1), wallet(8, "1", 1)}, nil),
		repo.EXPECT().ListWallets(gomock.Any(), int64(8), 2).Return([]domain.WalletSnapshot{wallet(9, "1", 1)}, nil),
	)
	cache.EXPECT().GetCachedBalances(gomock.Any(), gomock.Any()).Return(map[int64]int64{}, nil).Times(2)

	report, err := uc.Run(context.Background(), testOpts)
	require.NoError(t, err)
	assert.Equal(t, 3, report.WalletsScanned)
}

func TestReconcile_RepositoryError_Propagates(t *testing.T) {
	repo, _, uc := setupReconcile(t)

	repo.EXPECT().FindLedgerIssues(gomock.Any(), 100).Return(nil, errors.New("db down"))

	_, err := uc.Run(context.Background(), testOpts)
	require.ErrorContains(t, err, "db down")
}
