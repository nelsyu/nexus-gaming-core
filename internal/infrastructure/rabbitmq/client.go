package rabbitmq

import (
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/pkg/logger"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	ExchangeMain       = "nexus.events"
	ExchangeDLX        = "nexus.events.dlx"
	RoutingKeyTx       = "wallet.transaction.completed"
	RoutingKeyComp     = "wallet.transaction.compensation"
	QueueMain          = "wallet.transaction.completed.queue"
	QueueDLQ           = "wallet.transaction.dlq"

	// 重試等待區：訊息在 QueueRetry 停留 RetryDelay 後過期，
	// 透過 Dead Letter 機制自動回到 ExchangeMain (保留原 routing key)，再次被 Worker 消費。
	ExchangeRetry = "nexus.events.retry"
	QueueRetry    = "wallet.transaction.retry.queue"
	RetryDelay    = 5 * time.Second
)

type Client struct {
	Conn    *amqp.Connection
	Channel *amqp.Channel
}

// InitRabbitMQ 初始化連線並設定 Main Queue 與 DLQ 機制
func InitRabbitMQ(url string) (*Client, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open a channel: %w", err)
	}

	// 1. 宣告死信交換機 (DLX) 與 死信隊列 (DLQ)
	err = ch.ExchangeDeclare(ExchangeDLX, "direct", true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to declare DLX: %w", err)
	}

	_, err = ch.QueueDeclare(QueueDLQ, true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to declare DLQ: %w", err)
	}

	err = ch.QueueBind(QueueDLQ, RoutingKeyTx, ExchangeDLX, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to bind DLQ: %w", err)
	}

	// 2. 宣告主交換機 (Main Exchange) 與 主隊列 (Main Queue)
	err = ch.ExchangeDeclare(ExchangeMain, "direct", true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to declare main exchange: %w", err)
	}

	// 在宣告主隊列時，綁定死信交換機 (x-dead-letter-exchange)
	// 當訊息被 Nack 且 requeue=false 時，RabbitMQ 會自動將訊息轉發到這個 DLX
	args := amqp.Table{
		"x-dead-letter-exchange":    ExchangeDLX,
		"x-dead-letter-routing-key": RoutingKeyTx,
	}
	_, err = ch.QueueDeclare(QueueMain, true, false, false, false, args)
	if err != nil {
		return nil, fmt.Errorf("failed to declare main queue: %w", err)
	}

	err = ch.QueueBind(QueueMain, RoutingKeyTx, ExchangeMain, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to bind main queue for tx: %w", err)
	}

	err = ch.QueueBind(QueueMain, RoutingKeyComp, ExchangeMain, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to bind main queue for comp: %w", err)
	}

	// 3. 宣告重試等待區 (Retry Exchange + 帶 TTL 的 Retry Queue)
	// 不設定 x-dead-letter-routing-key，過期時會沿用訊息原本的 routing key 回到主交換機。
	err = ch.ExchangeDeclare(ExchangeRetry, "direct", true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to declare retry exchange: %w", err)
	}

	retryArgs := amqp.Table{
		"x-message-ttl":          RetryDelay.Milliseconds(),
		"x-dead-letter-exchange": ExchangeMain,
	}
	_, err = ch.QueueDeclare(QueueRetry, true, false, false, false, retryArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to declare retry queue: %w", err)
	}

	for _, key := range []string{RoutingKeyTx, RoutingKeyComp} {
		if err = ch.QueueBind(QueueRetry, key, ExchangeRetry, false, nil); err != nil {
			return nil, fmt.Errorf("failed to bind retry queue for %s: %w", key, err)
		}
	}

	logger.GetLogger().Info("Successfully connected to RabbitMQ and configured DLQ and retry queue")

	return &Client{
		Conn:    conn,
		Channel: ch,
	}, nil
}

func (c *Client) Close() {
	if c.Channel != nil {
		c.Channel.Close()
	}
	if c.Conn != nil {
		c.Conn.Close()
	}
}
