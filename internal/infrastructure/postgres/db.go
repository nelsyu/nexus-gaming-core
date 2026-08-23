package postgres

import (
	"context"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver
	"github.com/jmoiron/sqlx"
)

// InitDB 初始化資料庫連線池
func InitDB(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to db failed: %w", err)
	}

	// 設定連線池參數 (高併發環境必備)
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(20)
	db.SetConnMaxLifetime(time.Hour)
	db.SetConnMaxIdleTime(time.Minute * 10)

	return db, nil
}

// txKey 是一個自定義類型，用來在 context 中傳遞 sqlx.Tx
type txKey struct{}

// InjectTx 是一個輔助函數，將 *sqlx.Tx 注入到 context 中
func InjectTx(ctx context.Context, tx *sqlx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// ExtractTx 是一個輔助函數，從 context 中提取 *sqlx.Tx (如果有的話)
func ExtractTx(ctx context.Context) *sqlx.Tx {
	if tx, ok := ctx.Value(txKey{}).(*sqlx.Tx); ok {
		return tx
	}
	return nil
}
