package domain

import (
	"context"
)

// UnitOfWork 封裝了資料庫交易邊界
// 實作層可以透過 context 提取 tx
type UnitOfWork interface {
	DoTx(ctx context.Context, fn func(txCtx context.Context) error) error
}
