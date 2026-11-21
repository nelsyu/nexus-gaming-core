package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/jmoiron/sqlx"
)

type providerRepo struct {
	db *sqlx.DB
}

// NewProviderRepository 建立 ProviderRepository
func NewProviderRepository(db *sqlx.DB) domain.ProviderRepository {
	return &providerRepo{db: db}
}

func (r *providerRepo) GetByID(ctx context.Context, providerID string) (*domain.GameProvider, error) {
	const query = `SELECT provider_id, name, api_secret, allowed_ips, enabled FROM game_providers WHERE provider_id = $1`
	var p domain.GameProvider
	if err := sqlx.GetContext(ctx, r.db, &p, query, providerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrProviderNotFound
		}
		return nil, fmt.Errorf("get provider: %w", err)
	}
	return &p, nil
}
