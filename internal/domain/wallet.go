package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Wallet 代表玩家的錢包餘額
type Wallet struct {
	ID        int64           `json:"id"`
	UserID    int64           `json:"user_id"`
	Balance   decimal.Decimal `json:"balance"`  // 使用 decimal 確保金額精準度
	Currency  string          `json:"currency"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Version   int             `json:"version"`  // 用於樂觀鎖控制 (Optimistic Locking)
}
