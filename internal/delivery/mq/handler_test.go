package mq

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/domain/mocks"
	"github.com/bosstest/nexus-core/internal/infrastructure/rabbitmq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// fakeAcknowledger 記錄 handler 最終對 broker 做了什麼回覆
type fakeAcknowledger struct {
	action  string // "ack" / "reject" / "nack"
	requeue bool
}

func (f *fakeAcknowledger) Ack(uint64, bool) error { f.action = "ack"; return nil }
func (f *fakeAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	f.action, f.requeue = "nack", requeue
	return nil
}
func (f *fakeAcknowledger) Reject(_ uint64, requeue bool) error {
	f.action, f.requeue = "reject", requeue
	return nil
}

// fakeRetryPublisher 記錄重試訊息被送出的次數與 retryCount
type fakeRetryPublisher struct {
	calls      int
	retryCount int
	err        error
}

func (f *fakeRetryPublisher) PublishRetry(_ context.Context, _ amqp.Delivery, retryCount int) error {
	f.calls++
	f.retryCount = retryCount
	return f.err
}

const testMaxRetries = 3

func newDelivery(t *testing.T, routingKey string, body any, retryCount int) (amqp.Delivery, *fakeAcknowledger) {
	t.Helper()
	raw, ok := body.([]byte)
	if !ok {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	ack := &fakeAcknowledger{}
	d := amqp.Delivery{Acknowledger: ack, RoutingKey: routingKey, Body: raw, Headers: amqp.Table{}}
	if retryCount > 0 {
		d.Headers[rabbitmq.RetryCountHeader] = int32(retryCount)
	}
	return d, ack
}

func compEvent() domain.CompensationEvent {
	return domain.CompensationEvent{UserID: 1, Currency: "TWD", ProviderID: "pgsoft", ProviderTxID: "tx1", Type: domain.TxTypeBet, Amount: decimal.NewFromInt(100)}
}

func TestHandle_CompensationSuccess_Acks(t *testing.T) {
	cache := mocks.NewMockWalletCache(gomock.NewController(t))
	retry := &fakeRetryPublisher{}
	h := NewHandler(cache, retry, testMaxRetries)

	cache.EXPECT().Invalidate(gomock.Any(), int64(1), "TWD", "pgsoft", "tx1").Return(nil)
	d, ack := newDelivery(t, rabbitmq.RoutingKeyComp, compEvent(), 0)

	h.Handle(context.Background(), d)

	assert.Equal(t, "ack", ack.action)
	assert.Zero(t, retry.calls)
}

// 核心案例：Redis 暫時不可用時，不再立刻 requeue (無限熱迴圈)，而是送進等待區並 Ack 原訊息
func TestHandle_TransientFailure_SchedulesRetryAndAcks(t *testing.T) {
	cache := mocks.NewMockWalletCache(gomock.NewController(t))
	retry := &fakeRetryPublisher{}
	h := NewHandler(cache, retry, testMaxRetries)

	cache.EXPECT().Invalidate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("redis down"))
	d, ack := newDelivery(t, rabbitmq.RoutingKeyComp, compEvent(), 1)

	h.Handle(context.Background(), d)

	assert.Equal(t, 1, retry.calls)
	assert.Equal(t, 2, retry.retryCount, "retry count should increase by one")
	assert.Equal(t, "ack", ack.action, "original message is acked only after retry is published")
}

func TestHandle_MaxRetriesExceeded_RejectsToDLQ(t *testing.T) {
	cache := mocks.NewMockWalletCache(gomock.NewController(t))
	retry := &fakeRetryPublisher{}
	h := NewHandler(cache, retry, testMaxRetries)

	cache.EXPECT().Invalidate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("redis down"))
	d, ack := newDelivery(t, rabbitmq.RoutingKeyComp, compEvent(), testMaxRetries)

	h.Handle(context.Background(), d)

	assert.Zero(t, retry.calls)
	assert.Equal(t, "reject", ack.action)
	assert.False(t, ack.requeue, "reject without requeue sends the message to the DLX")
}

// 送進等待區失敗時絕不能 Ack，否則訊息會遺失
func TestHandle_RetryPublishFails_NacksWithRequeue(t *testing.T) {
	cache := mocks.NewMockWalletCache(gomock.NewController(t))
	retry := &fakeRetryPublisher{err: errors.New("channel closed")}
	h := NewHandler(cache, retry, testMaxRetries)

	cache.EXPECT().Invalidate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("redis down"))
	d, ack := newDelivery(t, rabbitmq.RoutingKeyComp, compEvent(), 0)

	h.Handle(context.Background(), d)

	assert.Equal(t, "nack", ack.action)
	assert.True(t, ack.requeue)
}

func TestHandle_PermanentFailures_RejectWithoutRetry(t *testing.T) {
	tests := []struct {
		name       string
		routingKey string
		body       []byte
	}{
		{name: "malformed compensation event", routingKey: rabbitmq.RoutingKeyComp, body: []byte("{not json")},
		{name: "malformed transaction event", routingKey: rabbitmq.RoutingKeyTx, body: []byte("{not json")},
		{name: "unknown routing key", routingKey: "wallet.unknown", body: []byte("{}")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := mocks.NewMockWalletCache(gomock.NewController(t))
			retry := &fakeRetryPublisher{}
			h := NewHandler(cache, retry, testMaxRetries)

			d, ack := newDelivery(t, tt.routingKey, tt.body, 0)
			h.Handle(context.Background(), d)

			assert.Zero(t, retry.calls, "permanent failures must not be retried")
			assert.Equal(t, "reject", ack.action)
			assert.False(t, ack.requeue)
		})
	}
}

func TestHandle_TransactionCompleted(t *testing.T) {
	tests := []struct {
		name       string
		amount     int64
		wantAction string
		wantRetry  int
	}{
		{name: "normal event is acked", amount: 100, wantAction: "ack", wantRetry: 0},
		{name: "simulated failure (999) is retried", amount: 999, wantAction: "ack", wantRetry: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := mocks.NewMockWalletCache(gomock.NewController(t))
			retry := &fakeRetryPublisher{}
			h := NewHandler(cache, retry, testMaxRetries)

			ev := domain.TransactionCompletedEvent{TransactionID: "1", UserID: 1, Amount: decimal.NewFromInt(tt.amount)}
			d, ack := newDelivery(t, rabbitmq.RoutingKeyTx, ev, 0)
			h.Handle(context.Background(), d)

			assert.Equal(t, tt.wantAction, ack.action)
			assert.Equal(t, tt.wantRetry, retry.calls)
		})
	}
}

func TestRetryCount_HeaderTypes(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
	}{
		{name: "missing header", value: nil, want: 0},
		{name: "int32 (as published)", value: int32(2), want: 2},
		{name: "int64 (as some brokers decode)", value: int64(3), want: 3},
		{name: "unexpected type", value: "4", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := amqp.Delivery{Headers: amqp.Table{}}
			if tt.value != nil {
				d.Headers[rabbitmq.RetryCountHeader] = tt.value
			}
			assert.Equal(t, tt.want, rabbitmq.RetryCount(d))
		})
	}
}
