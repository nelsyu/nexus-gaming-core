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

type userRepo struct {
	db *sqlx.DB
}

// NewUserRepository 建立 UserRepository 實例
func NewUserRepository(db *sqlx.DB) domain.UserRepository {
	return &userRepo{db: db}
}

func (r *userRepo) getRunner(ctx context.Context) sqlx.ExtContext {
	if tx := ExtractTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

// CreateUser 新增使用者（全參數化查詢，防止 SQL Injection）
func (r *userRepo) CreateUser(ctx context.Context, user *domain.User) error {
	query := `
		INSERT INTO users (tenant_id, username, password_hash, role, currency)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at
	`
	runner := r.getRunner(ctx)
	err := runner.QueryRowxContext(ctx, query,
		user.TenantID,
		user.Username,
		user.PasswordHash,
		user.Role,
		user.Currency,
	).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return domain.ErrUserAlreadyExists
		}
		return err
	}
	return nil
}

// GetByID 透過 ID 查詢使用者
func (r *userRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	query := `
		SELECT id, tenant_id, username, password_hash, role, currency, created_at, updated_at
		FROM users
		WHERE id = $1
	`
	runner := r.getRunner(ctx)
	var user domain.User
	err := sqlx.GetContext(ctx, runner, &user, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

// GetByUsername 透過 tenant_id 與 username 查詢使用者
func (r *userRepo) GetByUsername(ctx context.Context, tenantID, username string) (*domain.User, error) {
	query := `
		SELECT id, tenant_id, username, password_hash, role, currency, created_at, updated_at
		FROM users
		WHERE tenant_id = $1 AND username = $2
	`
	runner := r.getRunner(ctx)
	var user domain.User
	err := sqlx.GetContext(ctx, runner, &user, query, tenantID, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}
