package rabbitmq

import (
	"fmt"
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

	logger.GetLogger().Info("Successfully connected to RabbitMQ and configured DLQ")

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
