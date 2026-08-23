package postgres

import (
	"context"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/jmoiron/sqlx"
)

type sqlxUnitOfWork struct {
	db *sqlx.DB
}

// NewUnitOfWork 建立一個封裝 sqlx 的 UnitOfWork
func NewUnitOfWork(db *sqlx.DB) domain.UnitOfWork {
	return &sqlxUnitOfWork{db: db}
}

// DoTx 開啟資料庫事務，執行傳入的 fn，並根據結果自動 Commit 或 Rollback
func (u *sqlxUnitOfWork) DoTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	tx, err := u.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction failed: %w", err)
	}

	// 發生 panic 時也要 Rollback
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p) // 重新丟出 panic
		}
	}()

	// 將 sqlxTx 注入到 context 中，供 Repository 提取
	txCtx := InjectTx(ctx, tx)

	err = fn(txCtx)
	if err != nil {
		// 業務邏輯或 SQL 執行發生錯誤，Rollback
		tx.Rollback()
		return err
	}

	// 執行 Commit
	if err := tx.Commit(); err != nil {
		// 回傳 domain.ErrCommitFailed 以利上層區分錯誤類型 (例如用於觸發補償)
		return fmt.Errorf("%w: %v", domain.ErrCommitFailed, err)
	}

	return nil
}
