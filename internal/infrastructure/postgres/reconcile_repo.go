package postgres

import (
	"context"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/jmoiron/sqlx"
)

type reconcileRepo struct {
	db *sqlx.DB
}

// NewReconcileRepository 建立對帳用的 Repository (唯讀)
func NewReconcileRepository(db *sqlx.DB) domain.ReconcileRepository {
	return &reconcileRepo{db: db}
}

// ledgerIssuesQuery 一次完成三項 DB 帳務檢查。
//
// 為什麼可以用 id 排序代表交易先後？同一個錢包的交易在 SELECT ... FOR UPDATE 鎖住期間才 INSERT，
// 下一筆交易必須等前一筆 commit 才能取得鎖，所以同一錢包內 id (BIGSERIAL) 的順序就是實際發生的順序。
//
// 單一 SQL 語句在 Postgres 中看到的是一致的快照，執行期間即使有新交易寫入也不會誤報。
//
// 扣款類型 ('BET', 'WITHDRAW') 必須與 domain.TxTypeBet / domain.TxTypeWithdraw 保持一致。
//
// 注意：此查詢會掃描整張 transactions 表，資料量大時應改為增量檢查 (例如只檢查最近 N 天，
// 並保存每個錢包最後檢查到的 transaction id)。
const ledgerIssuesQuery = `
WITH ordered AS (
	SELECT
		t.id,
		t.wallet_id,
		t.type,
		t.amount,
		t.balance_before,
		t.balance_after,
		LAG(t.balance_after) OVER (PARTITION BY t.wallet_id ORDER BY t.id)     AS prev_after,
		ROW_NUMBER()         OVER (PARTITION BY t.wallet_id ORDER BY t.id DESC) AS rn_desc
	FROM transactions t
)
SELECT check_name, wallet_id, transaction_id, detail FROM (
	-- ① 算術：交易前 ± 金額 = 交易後
	SELECT 'ARITHMETIC' AS check_name, o.wallet_id, o.id AS transaction_id,
	       format('type=%s before=%s amount=%s after=%s', o.type, o.balance_before, o.amount, o.balance_after) AS detail
	FROM ordered o
	WHERE o.balance_after <> CASE
		WHEN o.type IN ('BET', 'WITHDRAW') THEN o.balance_before - o.amount
		ELSE o.balance_before + o.amount
	END

	UNION ALL

	-- ② 接續：本筆交易前 = 上一筆交易後
	SELECT 'CHAIN_BREAK', o.wallet_id, o.id,
	       format('prev_after=%s before=%s', o.prev_after, o.balance_before)
	FROM ordered o
	WHERE o.prev_after IS NOT NULL AND o.prev_after <> o.balance_before

	UNION ALL

	-- ③ 錢包餘額 = 最後一筆交易後 (沒有任何流水的錢包無法判斷期初餘額，不檢查)
	SELECT 'WALLET_MISMATCH', w.id, o.id,
	       format('wallet_balance=%s last_tx_after=%s', w.balance, o.balance_after)
	FROM wallets w
	JOIN ordered o ON o.wallet_id = w.id AND o.rn_desc = 1
	WHERE w.balance <> o.balance_after
) issues
ORDER BY check_name, wallet_id, transaction_id
LIMIT $1
`

func (r *reconcileRepo) FindLedgerIssues(ctx context.Context, limit int) ([]domain.LedgerIssue, error) {
	var issues []domain.LedgerIssue
	if err := sqlx.SelectContext(ctx, r.db, &issues, ledgerIssuesQuery, limit); err != nil {
		return nil, fmt.Errorf("find ledger issues: %w", err)
	}
	return issues, nil
}

func (r *reconcileRepo) ListWallets(ctx context.Context, afterID int64, limit int) ([]domain.WalletSnapshot, error) {
	// Keyset 分頁 (WHERE id > ?) 而非 OFFSET：OFFSET 越往後越慢，且掃描期間有新增資料時會漏掉或重複
	const query = `
		SELECT id, user_id, currency, balance, version
		FROM wallets
		WHERE id > $1
		ORDER BY id
		LIMIT $2`
	var wallets []domain.WalletSnapshot
	if err := sqlx.SelectContext(ctx, r.db, &wallets, query, afterID, limit); err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	return wallets, nil
}

func (r *reconcileRepo) GetWalletsByIDs(ctx context.Context, ids []int64) ([]domain.WalletSnapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// sqlx.In 把 slice 展開成 (?, ?, ...)，Rebind 再轉成 Postgres 的 $1, $2 ...
	query, args, err := sqlx.In(`SELECT id, user_id, currency, balance, version FROM wallets WHERE id IN (?)`, ids)
	if err != nil {
		return nil, fmt.Errorf("build wallets query: %w", err)
	}
	var wallets []domain.WalletSnapshot
	if err := sqlx.SelectContext(ctx, r.db, &wallets, r.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("get wallets by ids: %w", err)
	}
	return wallets, nil
}
