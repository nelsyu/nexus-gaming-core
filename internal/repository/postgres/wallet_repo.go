package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/jmoiron/sqlx"
)

type walletRepo struct {
	db *sqlx.DB
}

// NewWalletRepository 建立 WalletRepository 實例
func NewWalletRepository(db *sqlx.DB) domain.WalletRepository {
	return &walletRepo{db: db}
}

// getRunner 根據 context 中是否有 tx 來決定使用 db 還是 tx 進行查詢
func (r *walletRepo) getRunner(ctx context.Context) sqlx.ExtContext {
	if tx := ExtractTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

func (r *walletRepo) CreateWallet(ctx context.Context, wallet *domain.Wallet) error {
	query := `
		INSERT INTO wallets (user_id, balance, currency, version)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at
	`
	runner := r.getRunner(ctx)
	err := runner.QueryRowxContext(ctx, query,
		wallet.UserID,
		wallet.Balance,
		wallet.Currency,
		wallet.Version,
	).Scan(&wallet.ID, &wallet.CreatedAt, &wallet.UpdatedAt)

	return err
}

func (r *walletRepo) GetWalletByID(ctx context.Context, id int64) (*domain.Wallet, error) {
	query := `SELECT id, user_id, balance, currency, created_at, updated_at, version FROM wallets WHERE id = $1`
	runner := r.getRunner(ctx)
	
	var w domain.Wallet
	err := sqlx.GetContext(ctx, runner, &w, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, err
	}
	return &w, nil
}

func (r *walletRepo) GetWalletByUserID(ctx context.Context, userID int64, currency string) (*domain.Wallet, error) {
	query := `SELECT id, user_id, balance, currency, created_at, updated_at, version FROM wallets WHERE user_id = $1 AND currency = $2`
	runner := r.getRunner(ctx)
	
	var w domain.Wallet
	err := sqlx.GetContext(ctx, runner, &w, query, userID, currency)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, err
	}
	return &w, nil
}

// GetWalletByIDForUpdate 使用悲觀鎖 (SELECT ... FOR UPDATE)
// 注意：呼叫此方法時，Context 必須已經被注入 Transaction (tx)
func (r *walletRepo) GetWalletByIDForUpdate(ctx context.Context, id int64) (*domain.Wallet, error) {
	query := `SELECT id, user_id, balance, currency, created_at, updated_at, version FROM wallets WHERE id = $1 FOR UPDATE`
	runner := r.getRunner(ctx)
	
	var w domain.Wallet
	err := sqlx.GetContext(ctx, runner, &w, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, err
	}
	return &w, nil
}

func (r *walletRepo) UpdateBalance(ctx context.Context, wallet *domain.Wallet) error {
	// 使用樂觀鎖：更新時檢查 version 是否一致，並將 version + 1
	query := `
		UPDATE wallets 
		SET balance = $1, version = version + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND version = $3
	`
	runner := r.getRunner(ctx)
	
	result, err := runner.ExecContext(ctx, query, wallet.Balance, wallet.ID, wallet.Version)
	if err != nil {
		return err
	}
	
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	
	if rowsAffected == 0 {
		return domain.ErrOptimisticLock
	}
	
	// 更新物件的 version
	wallet.Version++
	
	return nil
}
