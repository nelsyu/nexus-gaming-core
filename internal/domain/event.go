package domain

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// TransactionCompletedEvent 表示一筆扣款或派彩已在核心成功處理並落庫的事件
type TransactionCompletedEvent struct {
	TransactionID string          `json:"transaction_id"`
	ProviderID    string          `json:"provider_id"`
	ProviderTxID  string          `json:"provider_tx_id"`
	UserID        int64           `json:"user_id"`
	Currency      string          `json:"currency"`
	Type          TransactionType `json:"type"`
	Amount        decimal.Decimal `json:"amount"`
	BalanceAfter  decimal.Decimal `json:"balance_after"`
	Timestamp     time.Time       `json:"timestamp"`
}

// CompensationEvent 表示一筆交易在 Redis 預處理成功但 DB 落庫失敗，需要進行非同步補償的事件
type CompensationEvent struct {
	ProviderID   string          `json:"provider_id"`
	ProviderTxID string          `json:"provider_tx_id"`
	UserID       int64           `json:"user_id"`
	Currency     string          `json:"currency"`
	Type         TransactionType `json:"type"`   // 用於判斷補償方向：Debit 補加、Credit 補扣
	Amount       decimal.Decimal `json:"amount"` // 需要補償的金額
	Timestamp    time.Time       `json:"timestamp"`
}

// EventPublisher 定義發送領域事件的介面
type EventPublisher interface {
	PublishTransactionCompleted(ctx context.Context, event *TransactionCompletedEvent) error
	PublishCompensationEvent(ctx context.Context, event *CompensationEvent) error
}
