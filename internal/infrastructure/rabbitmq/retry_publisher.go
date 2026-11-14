package rabbitmq

import (
	"context"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RetryCountHeader 記錄訊息已被重試的次數。
// 刻意不用 "x-" 開頭：x- 前綴保留給 RabbitMQ 自己的 header (例如 x-death)。
const RetryCountHeader = "nexus-retry-count"

// RetryCount 從訊息 header 讀出已重試次數，沒有 header 代表第一次處理 (0)。
// 從 broker 收到的整數可能被解碼成 int32 或 int64，兩者都要處理。
func RetryCount(d amqp.Delivery) int {
	switch v := d.Headers[RetryCountHeader].(type) {
	case int32:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// RetryPublisher 把處理失敗的訊息送進重試等待區。
//
// 使用獨立的 Channel 並開啟 Publisher Confirm 模式：
// 呼叫端會在「確認 broker 收到重試訊息」之後才 Ack 原訊息，
// 若沒有 Confirm，Publish 回傳成功只代表寫進 TCP buffer，此時 Ack 原訊息可能導致訊息遺失。
type RetryPublisher struct {
	ch *amqp.Channel
}

// NewRetryPublisher 建立 RetryPublisher，會從既有連線開啟一個新的 Confirm 模式 Channel。
func NewRetryPublisher(client *Client) (*RetryPublisher, error) {
	ch, err := client.Conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open retry channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("failed to enable publisher confirms: %w", err)
	}
	return &RetryPublisher{ch: ch}, nil
}

// PublishRetry 將訊息以相同 routing key 送進重試等待區，並把重試次數寫入 header。
// 只有在 broker 確認收到 (ack) 後才回傳 nil。
func (p *RetryPublisher) PublishRetry(ctx context.Context, d amqp.Delivery, retryCount int) error {
	headers := amqp.Table{}
	for k, v := range d.Headers {
		headers[k] = v
	}
	headers[RetryCountHeader] = int32(retryCount)

	confirm, err := p.ch.PublishWithDeferredConfirmWithContext(ctx,
		ExchangeRetry,
		d.RoutingKey, // 保留原 routing key，過期回到主交換機時才能路由回原本的隊列
		false,
		false,
		amqp.Publishing{
			ContentType:  d.ContentType,
			Body:         d.Body,
			Headers:      headers,
			MessageId:    d.MessageId,
			Timestamp:    d.Timestamp,
			DeliveryMode: amqp.Persistent,
		})
	if err != nil {
		return fmt.Errorf("failed to publish retry message: %w", err)
	}

	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("failed waiting for retry publish confirm: %w", err)
	}
	if !acked {
		return errors.New("broker nacked retry publish")
	}
	return nil
}

// Close 關閉重試專用的 Channel
func (p *RetryPublisher) Close() {
	if p.ch != nil {
		_ = p.ch.Close()
	}
}
