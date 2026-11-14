package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bosstest/nexus-core/internal/delivery/mq"
	"github.com/bosstest/nexus-core/internal/infrastructure/rabbitmq"
	"github.com/bosstest/nexus-core/internal/infrastructure/redis"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

const (
	consumerTag = "worker-1"
	// shutdownTimeout 需小於 docker-compose 的 stop_grace_period，否則會在處理中被 SIGKILL
	shutdownTimeout = 20 * time.Second
	// maxRetries 進入 DLQ 前最多重試的次數 (每次間隔 rabbitmq.RetryDelay)
	maxRetries = 5
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

	retryPublisher, err := rabbitmq.NewRetryPublisher(rmqClient)
	if err != nil {
		logger.GetLogger().Fatal("Failed to initialize retry publisher", zap.Error(err))
	}
	defer retryPublisher.Close()

	handler := mq.NewHandler(walletCache, retryPublisher, maxRetries)

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
			handler.Handle(context.Background(), d)
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
