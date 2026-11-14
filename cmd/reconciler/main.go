// reconciler 對帳工具：執行一次後結束，適合由 cron / Kubernetes CronJob 定期排程。
//
// Exit code：
//
//	0 帳務一致
//	1 發現不一致 (排程系統應據此發出告警)
//	2 對帳本身執行失敗 (例如連不上 DB)
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bosstest/nexus-core/internal/infrastructure/postgres"
	"github.com/bosstest/nexus-core/internal/infrastructure/redis"
	"github.com/bosstest/nexus-core/internal/usecase"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

const (
	exitOK     = 0
	exitIssues = 1
	exitError  = 2
)

func main() {
	os.Exit(run())
}

// run 回傳 exit code。與 main 分開，確保 defer (關閉連線) 在 os.Exit 之前執行。
func run() int {
	healCache := flag.Bool("heal-cache", false, "刪除確認漂移的 Redis 快取 (預設只回報)")
	recheckDelay := flag.Duration("recheck-delay", 2*time.Second, "發現快取不一致後，等待多久再複查")
	batchSize := flag.Int("batch", 500, "每批掃描的錢包數量")
	issueLimit := flag.Int("limit", 100, "DB 帳務問題最多回報幾筆")
	timeout := flag.Duration("timeout", 10*time.Minute, "整次對帳的時間上限")
	flag.Parse()

	_ = godotenv.Load()
	log := logger.GetLogger()

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Error("DB_DSN environment variable is required")
		return exitError
	}
	sentinelAddrs := os.Getenv("REDIS_SENTINEL_ADDRS")
	if sentinelAddrs == "" {
		sentinelAddrs = "localhost:26379"
	}
	masterName := os.Getenv("REDIS_MASTER_NAME")
	if masterName == "" {
		masterName = "mymaster"
	}

	db, err := postgres.InitDB(dsn)
	if err != nil {
		log.Error("Failed to initialize database", zap.Error(err))
		return exitError
	}
	defer db.Close()

	rdb := redis.InitSentinelClient(sentinelAddrs, masterName)
	defer rdb.Close()

	// 收到 Ctrl+C / SIGTERM 時取消對帳，或超過時間上限
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	uc := usecase.NewReconcileUsecase(postgres.NewReconcileRepository(db), redis.NewCacheInspector(rdb))

	start := time.Now()
	report, err := uc.Run(ctx, usecase.ReconcileOptions{
		BatchSize:    *batchSize,
		IssueLimit:   *issueLimit,
		RecheckDelay: *recheckDelay,
		HealCache:    *healCache,
	})
	if err != nil {
		log.Error("Reconciliation failed", zap.Error(err))
		return exitError
	}

	for _, issue := range report.LedgerIssues {
		log.Error("Ledger inconsistency",
			zap.String("check", issue.Check),
			zap.Int64("wallet_id", issue.WalletID),
			zap.Int64("transaction_id", issue.TransactionID),
			zap.String("detail", issue.Detail),
		)
	}
	if len(report.LedgerIssues) >= *issueLimit {
		log.Warn("Ledger issue limit reached, more issues may exist", zap.Int("limit", *issueLimit))
	}
	for _, issue := range report.CacheIssues {
		log.Error("Wallet cache drift",
			zap.Int64("wallet_id", issue.WalletID),
			zap.Int64("user_id", issue.UserID),
			zap.String("currency", issue.Currency),
			zap.Int64("db_units", issue.DBUnits),
			zap.Int64("cache_units", issue.CacheUnits),
			zap.Bool("healed", issue.Healed),
		)
	}

	log.Info("Reconciliation finished",
		zap.Duration("elapsed", time.Since(start)),
		zap.Int("ledger_issues", len(report.LedgerIssues)),
		zap.Int("cache_issues", len(report.CacheIssues)),
		zap.Int("wallets_scanned", report.WalletsScanned),
		zap.Int("cache_compared", report.CacheCompared),
		zap.Int("cache_suspects", report.CacheSuspects),
		zap.Int("cache_skipped_busy", report.CacheSkippedBusy),
		zap.Int("cache_healed", report.CacheHealed),
	)

	if report.HasIssues() {
		return exitIssues
	}
	return exitOK
}
