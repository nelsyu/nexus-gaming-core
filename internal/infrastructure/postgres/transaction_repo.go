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
	query := `
		INSERT INTO transactions 
		(wallet_id, type, amount, balance_before, balance_after, provider_id, provider_tx_id, reference_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at
	`
	runner := r.getRunner(ctx)
	
	err := runner.QueryRowxContext(ctx, query,
		tx.WalletID,
		tx.Type,
		tx.Amount,
		tx.BalanceBefore,
		tx.BalanceAfter,
		tx.ProviderID,
		tx.ProviderTxID,
		tx.ReferenceID,
	).Scan(&tx.ID, &tx.CreatedAt)

	if err != nil {
		// 檢查是否為 PostgreSQL Unique Constraint 違反錯誤
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return domain.ErrDuplicateTransaction
		}
		return err
	}
	
	return nil
}

func (r *transactionRepo) GetByProviderTxID(ctx context.Context, providerID, providerTxID string) (*domain.Transaction, error) {
	query := `
		SELECT id, wallet_id, type, amount, balance_before, balance_after, provider_id, provider_tx_id, reference_id, created_at 
		FROM transactions 
		WHERE provider_id = $1 AND provider_tx_id = $2
	`
	runner := r.getRunner(ctx)
	
	var tx domain.Transaction
	err := sqlx.GetContext(ctx, runner, &tx, query, providerID, providerTxID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // 找不到不當作錯誤，回傳 nil 表示可繼續處理
		}
		return nil, err
	}
	
	return &tx, nil
}
