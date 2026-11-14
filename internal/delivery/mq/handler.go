// Package mq 負責消費 RabbitMQ 訊息，角色等同 HTTP 的 handler：
// 解析訊息、呼叫對應邏輯，並根據結果決定 Ack / 重試 / 送進 DLQ。
package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/infrastructure/rabbitmq"
	"github.com/bosstest/nexus-core/pkg/logger"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// ErrPermanent 代表「重試也不會成功」的錯誤 (例如 JSON 格式錯誤、未知的 routing key)，
// 這類訊息不進重試等待區，直接送進 DLQ。
var ErrPermanent = errors.New("permanent failure")

// processTimeout 單則訊息處理的時間上限
const processTimeout = 5 * time.Second

// RetryPublisher 把失敗的訊息送進重試等待區。
// 只有在 broker 確認收到後才回傳 nil。
type RetryPublisher interface {
	PublishRetry(ctx context.Context, d amqp.Delivery, retryCount int) error
}

// Handler 處理主隊列中的所有訊息
type Handler struct {
	cache      domain.WalletCache
	retry      RetryPublisher
	maxRetries int
}

// NewHandler 建立 Handler。maxRetries 為進入 DLQ 前最多重試的次數。
func NewHandler(cache domain.WalletCache, retry RetryPublisher, maxRetries int) *Handler {
	return &Handler{cache: cache, retry: retry, maxRetries: maxRetries}
}

// Handle 處理一則訊息，並負責回覆 broker (Ack / Reject / Nack)。
//
//	成功                   → Ack
//	永久性錯誤             → Reject → DLQ
//	暫時性錯誤、未達上限   → 送進重試等待區 (延遲後自動回來)，確認送達後 Ack 原訊息
//	暫時性錯誤、已達上限   → Reject → DLQ
func (h *Handler) Handle(ctx context.Context, d amqp.Delivery) {
	err := h.process(ctx, d)
	if err == nil {
		_ = d.Ack(false)
		return
	}

	retryCount := rabbitmq.RetryCount(d)
	log := logger.GetLogger().With(
		zap.String("routing_key", d.RoutingKey),
		zap.Int("retry_count", retryCount),
		zap.Error(err),
	)

	switch {
	case errors.Is(err, ErrPermanent):
		log.Error("Permanent failure, moving message to DLQ")
		_ = d.Reject(false)

	case retryCount >= h.maxRetries:
		log.Error("Max retries exceeded, moving message to DLQ", zap.Int("max_retries", h.maxRetries))
		_ = d.Reject(false)

	default:
		if pubErr := h.retry.PublishRetry(ctx, d, retryCount+1); pubErr != nil {
			// 送不進等待區 (通常代表 RabbitMQ 本身有問題)。不能 Ack，否則訊息會遺失；
			// 退回原隊列，交由 broker 重新投遞。
			log.Error("Failed to schedule retry, requeueing message", zap.NamedError("publish_error", pubErr))
			_ = d.Nack(false, true)
			return
		}
		log.Warn("Processing failed, retry scheduled", zap.Duration("delay", rabbitmq.RetryDelay))
		// 已確認重試訊息送達 broker，才能 Ack 原訊息
		_ = d.Ack(false)
	}
}

func (h *Handler) process(ctx context.Context, d amqp.Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, processTimeout)
	defer cancel()

	switch d.RoutingKey {
	case rabbitmq.RoutingKeyComp:
		return h.handleCompensation(ctx, d.Body)
	case rabbitmq.RoutingKeyTx:
		return h.handleTransactionCompleted(d.Body)
	default:
		return fmt.Errorf("%w: unknown routing key %q", ErrPermanent, d.RoutingKey)
	}
}

// handleCompensation 刪除錢包快取，讓下一個請求從 DB 重建正確餘額 (不做金額反向加減)。
func (h *Handler) handleCompensation(ctx context.Context, body []byte) error {
	var ev domain.CompensationEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return fmt.Errorf("%w: unmarshal compensation event: %v", ErrPermanent, err)
	}

	if err := h.cache.Invalidate(ctx, ev.UserID, ev.Currency, ev.ProviderID, ev.ProviderTxID); err != nil {
		return fmt.Errorf("invalidate wallet cache: %w", err) // 暫時性錯誤 (例如 Redis 不可用)，可重試
	}

	logger.GetLogger().Info("Cache invalidation done",
		zap.String("type", string(ev.Type)),
		zap.Int64("user_id", ev.UserID),
		zap.String("provider_tx_id", ev.ProviderTxID),
		zap.String("amount", ev.Amount.String()),
	)
	return nil
}

// simulatedFailureAmount 金額為此值時模擬處理失敗，用於展示重試與 DLQ 流程
var simulatedFailureAmount = decimal.NewFromInt(999)

// handleTransactionCompleted 處理交易完成事件 (目前為模擬：寫報表、算返水等)。
func (h *Handler) handleTransactionCompleted(body []byte) error {
	var ev domain.TransactionCompletedEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return fmt.Errorf("%w: unmarshal transaction completed event: %v", ErrPermanent, err)
	}

	// [人為模擬錯誤]：金額 999 時視為暫時性失敗，會經過數次重試後進入 DLQ
	if ev.Amount.Equal(simulatedFailureAmount) {
		return fmt.Errorf("simulated processing failure for transaction %s", ev.TransactionID)
	}

	logger.GetLogger().Info("Successfully processed event",
		zap.String("transaction_id", ev.TransactionID),
		zap.Int64("user_id", ev.UserID),
		zap.String("amount", ev.Amount.String()),
	)
	return nil
}
