package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/infrastructure/rabbitmq"
	"github.com/bosstest/nexus-core/internal/infrastructure/redis"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

const (
	consumerTag = "worker-1"
	// shutdownTimeout 需小於 docker-compose 的 stop_grace_period，否則會在處理中被 SIGKILL
	shutdownTimeout = 20 * time.Second
)

func main() {
	_ = godotenv.Load()

	rabbitMQUrl := os.Getenv("RABBITMQ_URL")
	if rabbitMQUrl == "" {
		rabbitMQUrl = "amqp://guest:guest@localhost:5672/"
	}

	sentinelAddrs := os.Getenv("REDIS_SENTINEL_ADDRS")
	if sentinelAddrs == "" {
		sentinelAddrs = "localhost:26379"
	}
	masterName := os.Getenv("REDIS_MASTER_NAME")
	if masterName == "" {
		masterName = "mymaster"
	}

	// 1. 初始化 Redis Sentinel 連線
	rdb := redis.InitSentinelClient(sentinelAddrs, masterName)
	defer rdb.Close()
	walletCache := redis.NewWalletCache(rdb)

	// 2. 初始化 RabbitMQ
	rmqClient, err := rabbitmq.InitRabbitMQ(rabbitMQUrl)
	if err != nil {
		logger.GetLogger().Fatal("Failed to initialize RabbitMQ", zap.Error(err))
	}
	defer rmqClient.Close()

	// 3. 設定 Prefetch：一次最多只拿 20 則未確認訊息。
	// 若不設定，RabbitMQ 會把整個 Queue 塞進本機 buffer，關機時這些訊息都得重新投遞。
	if err := rmqClient.Channel.Qos(20, 0, false); err != nil {
		logger.GetLogger().Fatal("Failed to set QoS", zap.Error(err))
	}

	// 4. 建立 Consumer
	msgs, err := rmqClient.Channel.Consume(
		rabbitmq.QueueMain, // queue
		consumerTag,        // consumer id
		false,              // auto-ack 關閉 (手動確認)
		false,              // exclusive
		false,              // no-local
		false,              // no-wait
		nil,                // args
	)
	if err != nil {
		logger.GetLogger().Fatal("Failed to register a consumer", zap.Error(err))
	}

	logger.GetLogger().Info("Worker Started. Waiting for messages...")

	// 5. 在背景依序處理訊息；msgs 被關閉 (Consumer 取消) 時迴圈結束並通知 done
	done := make(chan struct{})
	go func() {
		defer close(done)
		for d := range msgs {
			processMessage(d, walletCache)
		}
	}()

	// 6. 優雅關閉：收到訊號後先停止接收新訊息，再等手上的訊息處理完
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	logger.GetLogger().Info("Shutting down gracefully: cancelling consumer...")

	if err := rmqClient.Channel.Cancel(consumerTag, false); err != nil {
		logger.GetLogger().Error("Failed to cancel consumer", zap.Error(err))
	}

	select {
	case <-done:
		logger.GetLogger().Info("All in-flight messages processed, worker stopped")
	case <-time.After(shutdownTimeout):
		// 超時未處理完的訊息尚未 Ack，連線關閉後 RabbitMQ 會自動重新投遞，不會遺失
		logger.GetLogger().Warn("Shutdown timeout reached, unacked messages will be redelivered")
	}
}

func processMessage(d amqp.Delivery, cache domain.WalletCache) {
	if d.RoutingKey == rabbitmq.RoutingKeyComp {
		var compEvent domain.CompensationEvent
		if err := json.Unmarshal(d.Body, &compEvent); err != nil {
			logger.GetLogger().Error("Failed to unmarshal compensation event", zap.Error(err))
			_ = d.Reject(false)
			return
		}

		// 只刪除快取，不做金額反向加減：下一個請求會從 DB (唯一可信來源) 重建正確餘額
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cache.Invalidate(ctx, compEvent.UserID, compEvent.Currency, compEvent.ProviderID, compEvent.ProviderTxID)
		cancel()
		if err != nil {
			logger.GetLogger().Error("Failed to invalidate wallet cache", zap.Error(err))
			_ = d.Nack(false, true) // 重試
			return
		}

		logger.GetLogger().Info("Cache invalidation done",
			zap.String("type", string(compEvent.Type)),
			zap.Int64("user_id", compEvent.UserID),
			zap.String("provider_tx_id", compEvent.ProviderTxID),
			zap.String("amount", compEvent.Amount.String()),
		)
		_ = d.Ack(false)
		return
	}

	var event domain.TransactionCompletedEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		logger.GetLogger().Error("Failed to unmarshal message, discarding", zap.Error(err))
		_ = d.Reject(false) // 格式錯誤，不要重試，直接丟掉或進 DLQ
		return
	}

	// [人為模擬錯誤]：如果金額是 999，我們模擬處理失敗，將訊息 Nack 且不重新入隊 (requeue=false)
	// 這樣 RabbitMQ 就會自動根據我們在宣告時綁定的設定，把它踢進 Dead Letter Queue (DLQ)
	if event.Amount.Equal(decimal.NewFromInt(999)) {
		logger.GetLogger().Warn("Simulated Error: Rejecting transaction, moving to DLQ...", zap.String("transaction_id", event.TransactionID))
		_ = d.Nack(false, false)
		return
	}

	// 模擬正常的業務處理 (如：寫報表、算返水)
	logger.GetLogger().Info("Successfully processed event",
		zap.String("transaction_id", event.TransactionID),
		zap.Int64("user_id", event.UserID),
		zap.String("amount", event.Amount.String()),
	)
	_ = d.Ack(false)
}
