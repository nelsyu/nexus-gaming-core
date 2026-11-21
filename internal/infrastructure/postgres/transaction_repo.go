package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// constraintRefundTarget 「一筆 BET 只能退款一次」的唯一索引名稱 (見 migrations)
const constraintRefundTarget = "uq_transactions_refund_target"

// transactionColumns 查詢流水帳的欄位。
// round_id / related_provider_tx_id 可能為 NULL，用 COALESCE 轉成空字串，才能掃描進 Go 的 string。
const transactionColumns = `
	id, wallet_id, type, amount, balance_before, balance_after,
	provider_id, provider_tx_id, COALESCE(reference_id, '') AS reference_id,
	COALESCE(round_id, '') AS round_id,
	COALESCE(related_provider_tx_id, '') AS related_provider_tx_id,
	created_at`

type transactionRepo struct {
	db *sqlx.DB
}

// NewTransactionRepository 建立 TransactionRepository 實例
func NewTransactionRepository(db *sqlx.DB) domain.TransactionRepository {
	return &transactionRepo{db: db}
}

func (r *transactionRepo) getRunner(ctx context.Context) sqlx.ExtContext {
	if tx := ExtractTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

func (r *transactionRepo) CreateTransaction(ctx context.Context, tx *domain.Transaction) error {
	// NULLIF：空字串存成 NULL，讓「沒有 round」與「round 是空字串」在 DB 中語意一致
	query := `
		INSERT INTO transactions
		(wallet_id, type, amount, balance_before, balance_after, provider_id, provider_tx_id,
		 reference_id, round_id, related_provider_tx_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''))
		RETURNING id, created_at
	`
	err := r.getRunner(ctx).QueryRowxContext(ctx, query,
		tx.WalletID,
		tx.Type,
		tx.Amount,
		tx.BalanceBefore,
		tx.BalanceAfter,
		tx.ProviderID,
		tx.ProviderTxID,
		tx.ReferenceID,
		tx.RoundID,
		tx.RelatedProviderTxID,
	).Scan(&tx.ID, &tx.CreatedAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			// 同樣是唯一鍵衝突，用約束名稱區分原因
			if pgErr.ConstraintName == constraintRefundTarget {
				return domain.ErrBetAlreadyRefunded
			}
			return domain.ErrDuplicateTransaction
		}
		return err
	}

	return nil
}

func (r *transactionRepo) GetByProviderTxID(ctx context.Context, providerID, providerTxID string) (*domain.Transaction, error) {
	query := `SELECT ` + transactionColumns + ` FROM transactions WHERE provider_id = $1 AND provider_tx_id = $2`
	return r.getOne(ctx, query, providerID, providerTxID)
}

func (r *transactionRepo) GetRefundByTarget(ctx context.Context, providerID, betProviderTxID string) (*domain.Transaction, error) {
	query := `SELECT ` + transactionColumns + ` FROM transactions
		WHERE provider_id = $1 AND related_provider_tx_id = $2 AND type = 'REFUND'`
	return r.getOne(ctx, query, providerID, betProviderTxID)
}

// getOne 查詢單筆流水，找不到回傳 nil, nil
func (r *transactionRepo) getOne(ctx context.Context, query string, args ...any) (*domain.Transaction, error) {
	var tx domain.Transaction
	if err := sqlx.GetContext(ctx, r.getRunner(ctx), &tx, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &tx, nil
}

func (r *transactionRepo) HasActiveBetInRound(ctx context.Context, walletID int64, providerID, roundID string) (bool, error) {
	// 「尚未被退款」：找不到任何指向這筆 BET 的 REFUND
	query := `
		SELECT EXISTS (
			SELECT 1 FROM transactions b
			WHERE b.wallet_id = $1 AND b.provider_id = $2 AND b.round_id = $3 AND b.type = 'BET'
			  AND NOT EXISTS (
				SELECT 1 FROM transactions r
				WHERE r.provider_id = b.provider_id
				  AND r.related_provider_tx_id = b.provider_tx_id
				  AND r.type = 'REFUND'
			  )
		)`
	return r.exists(ctx, query, walletID, providerID, roundID)
}

func (r *transactionRepo) HasWinInRound(ctx context.Context, walletID int64, providerID, roundID string) (bool, error) {
	query := `
		SELECT EXISTS (
			SELECT 1 FROM transactions
			WHERE wallet_id = $1 AND provider_id = $2 AND round_id = $3 AND type = 'WIN'
		)`
	return r.exists(ctx, query, walletID, providerID, roundID)
}

func (r *transactionRepo) VoidBet(ctx context.Context, walletID int64, providerID, betProviderTxID, voidedByProviderTxID string) error {
	// ON CONFLICT DO NOTHING：同一個退款請求重送時不報錯 (冪等)
	query := `
		INSERT INTO voided_bets (provider_id, provider_tx_id, voided_by_provider_tx_id, wallet_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider_id, provider_tx_id) DO NOTHING`
	_, err := r.getRunner(ctx).ExecContext(ctx, query, providerID, betProviderTxID, voidedByProviderTxID, walletID)
	return err
}

func (r *transactionRepo) IsBetVoided(ctx context.Context, providerID, betProviderTxID string) (bool, error) {
	query := `SELECT EXISTS (SELECT 1 FROM voided_bets WHERE provider_id = $1 AND provider_tx_id = $2)`
	return r.exists(ctx, query, providerID, betProviderTxID)
}

func (r *transactionRepo) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var ok bool
	if err := r.getRunner(ctx).QueryRowxContext(ctx, query, args...).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}
