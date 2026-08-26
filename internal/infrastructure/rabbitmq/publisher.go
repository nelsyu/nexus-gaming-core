package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/pkg/logger"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

type EventPublisher struct {
	channel *amqp.Channel
}

func NewEventPublisher(client *Client) domain.EventPublisher {
	return &EventPublisher{
		channel: client.Channel,
	}
}

func (p *EventPublisher) PublishTransactionCompleted(ctx context.Context, event *domain.TransactionCompletedEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	err = p.channel.PublishWithContext(ctx,
		ExchangeMain, // exchange
		RoutingKeyTx, // routing key
		false,        // mandatory
		false,        // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent, // 訊息持久化
		})

	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	logger.GetLogger().Info("Sent TransactionCompletedEvent", zap.String("txID", event.TransactionID))
	return nil
}

func (p *EventPublisher) PublishCompensationEvent(ctx context.Context, event *domain.CompensationEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal compensation event: %w", err)
	}

	err = p.channel.PublishWithContext(ctx,
		ExchangeMain,   // exchange
		RoutingKeyComp, // routing key
		false,          // mandatory
		false,          // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent, // 訊息持久化
		})

	if err != nil {
		return fmt.Errorf("failed to publish compensation message: %w", err)
	}

	logger.GetLogger().Info("Sent CompensationEvent", zap.String("providerTxID", event.ProviderTxID))
	return nil
}
