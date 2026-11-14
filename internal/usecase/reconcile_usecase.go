package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/logger"
	"go.uber.org/zap"
)

// ReconcileOptions 對帳參數
type ReconcileOptions struct {
	// BatchSize 每批掃描的錢包數量
	BatchSize int
	// IssueLimit DB 帳務問題最多回報幾筆 (避免資料嚴重損壞時輸出爆量)
	IssueLimit int
	// RecheckDelay 發現快取不一致後，等待多久再確認一次，用來排除進行中的交易
	RecheckDelay time.Duration
	// HealCache 是否刪除確認漂移的快取。預設 false：對帳工具預設只讀、只回報
	HealCache bool
}

// ReconcileReport 對帳結果
type ReconcileReport struct {
	LedgerIssues []domain.LedgerIssue
	CacheIssues  []domain.CacheIssue

	WalletsScanned   int // 掃描的錢包數
	CacheCompared    int // 有快取、實際比對的錢包數
	CacheSuspects    int // 第一次比對不一致的錢包數
	CacheSkippedBusy int // 複查期間 DB 有新交易、無法判定而跳過的錢包數
	CacheHealed      int // 已刪除快取的錢包數
}

// HasIssues 是否發現任何不一致
func (r *ReconcileReport) HasIssues() bool {
	return len(r.LedgerIssues) > 0 || len(r.CacheIssues) > 0
}

// ReconcileUsecase 對帳：檢查 DB 帳務內部一致性，以及 Redis 快取與 DB 是否一致
type ReconcileUsecase struct {
	repo  domain.ReconcileRepository
	cache domain.CacheInspector
}

// NewReconcileUsecase 建立 ReconcileUsecase
func NewReconcileUsecase(repo domain.ReconcileRepository, cache domain.CacheInspector) *ReconcileUsecase {
	return &ReconcileUsecase{repo: repo, cache: cache}
}

// Run 執行一次完整對帳
func (u *ReconcileUsecase) Run(ctx context.Context, opts ReconcileOptions) (*ReconcileReport, error) {
	report := &ReconcileReport{}

	// 1. DB 帳務檢查：流水算術、流水接續、錢包對最後一筆流水
	ledgerIssues, err := u.repo.FindLedgerIssues(ctx, opts.IssueLimit)
	if err != nil {
		return nil, err
	}
	report.LedgerIssues = ledgerIssues

	// 2. 快取檢查第一輪：掃描所有錢包，找出快取與 DB 不一致的嫌疑名單
	suspects, err := u.scanCache(ctx, opts.BatchSize, report)
	if err != nil {
		return nil, err
	}
	report.CacheSuspects = len(suspects)
	if len(suspects) == 0 {
		return report, nil
	}

	// 3. 快取檢查第二輪：等一下再複查，排除「Redis 已預扣、DB 尚未 commit」的進行中交易
	if err := sleepCtx(ctx, opts.RecheckDelay); err != nil {
		return nil, err
	}
	if err := u.recheckSuspects(ctx, suspects, opts.HealCache, report); err != nil {
		return nil, err
	}

	return report, nil
}

func (u *ReconcileUsecase) scanCache(ctx context.Context, batchSize int, report *ReconcileReport) ([]domain.WalletSnapshot, error) {
	var suspects []domain.WalletSnapshot
	var afterID int64

	for {
		wallets, err := u.repo.ListWallets(ctx, afterID, batchSize)
		if err != nil {
			return nil, err
		}
		if len(wallets) == 0 {
			break
		}
		report.WalletsScanned += len(wallets)

		cached, err := u.cache.GetCachedBalances(ctx, wallets)
		if err != nil {
			return nil, err
		}

		for _, w := range wallets {
			cacheUnits, ok := cached[w.ID]
			if !ok {
				continue // 沒有快取，沒得比對
			}
			report.CacheCompared++

			dbUnits, err := domain.ToMinorUnits(w.Balance)
			if err != nil {
				return nil, fmt.Errorf("wallet %d balance %s cannot be converted to minor units: %w", w.ID, w.Balance, err)
			}
			if dbUnits != cacheUnits {
				suspects = append(suspects, w)
			}
		}

		afterID = wallets[len(wallets)-1].ID
		if len(wallets) < batchSize {
			break
		}
	}
	return suspects, nil
}

func (u *ReconcileUsecase) recheckSuspects(ctx context.Context, suspects []domain.WalletSnapshot, heal bool, report *ReconcileReport) error {
	ids := make([]int64, len(suspects))
	for i, s := range suspects {
		ids[i] = s.ID
	}

	fresh, err := u.repo.GetWalletsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	freshByID := make(map[int64]domain.WalletSnapshot, len(fresh))
	for _, w := range fresh {
		freshByID[w.ID] = w
	}

	freshCached, err := u.cache.GetCachedBalances(ctx, fresh)
	if err != nil {
		return err
	}

	for _, s := range suspects {
		w, ok := freshByID[s.ID]
		if !ok {
			continue // 錢包已被刪除
		}
		if w.Version != s.Version {
			// 複查期間 DB 有新交易：快取和 DB 都在變動，無法判定，留給下一次對帳
			report.CacheSkippedBusy++
			continue
		}
		cacheUnits, ok := freshCached[w.ID]
		if !ok {
			continue // 快取已過期或已被刪除，下一個請求會從 DB 重建，問題自然消失
		}
		dbUnits, err := domain.ToMinorUnits(w.Balance)
		if err != nil {
			return fmt.Errorf("wallet %d balance %s cannot be converted to minor units: %w", w.ID, w.Balance, err)
		}
		if dbUnits == cacheUnits {
			continue // 只是進行中的交易，已經一致
		}

		issue := domain.CacheIssue{
			WalletID:   w.ID,
			UserID:     w.UserID,
			Currency:   w.Currency,
			DBUnits:    dbUnits,
			CacheUnits: cacheUnits,
		}
		if heal {
			// 刪除快取是安全的修復：DB 是唯一可信來源，下一個請求會從 DB 重建
			if err := u.cache.InvalidateWallet(ctx, w.UserID, w.Currency); err != nil {
				logger.WithTraceID(ctx).Error("Failed to heal drifted wallet cache",
					zap.Int64("wallet_id", w.ID), zap.Error(err))
			} else {
				issue.Healed = true
				report.CacheHealed++
			}
		}
		report.CacheIssues = append(report.CacheIssues, issue)
	}
	return nil
}

// sleepCtx 等待 d，若 ctx 先被取消則提早回傳錯誤；d <= 0 時不等待
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
