package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/bosstest/nexus-core/internal/repository/rabbitmq"
	"github.com/bosstest/nexus-core/internal/repository/redis"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/shopspring/decimal"
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
		log.Fatalf("Failed to initialize RabbitMQ: %v", err)
	}
	defer rmqClient.Close()

	// 3. 建立 Consumer
	msgs, err := rmqClient.Channel.Consume(
		rabbitmq.QueueMain, // queue
		"worker-1",         // consumer id
		false,              // auto-ack 關閉 (手動確認)
		false,              // exclusive
		false,              // no-local
		false,              // no-wait
		nil,                // args
	)
	if err != nil {
		log.Fatalf("Failed to register a consumer: %v", err)
	}

	log.Println("[Worker] Started. Waiting for messages...")

	// 4. 使用 Context 與 Channel 優雅關閉
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 捕捉 Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case d, ok := <-msgs:
				if !ok {
					return
				}
				processMessage(d, walletCache)
			}
		}
	}()

	<-sigChan
	log.Println("[Worker] Shutting down gracefully...")
}

func processMessage(d amqp.Delivery, cache *redis.WalletCache) {
	if d.RoutingKey == rabbitmq.RoutingKeyComp {
		var compEvent domain.CompensationEvent
		if err := json.Unmarshal(d.Body, &compEvent); err != nil {
			log.Printf("[Error] Failed to unmarshal compensation event: %v", err)
			_ = d.Reject(false)
			return
		}

		amountFloat, _ := compEvent.Amount.Float64()
		// 根據原始操作的方向決定補償方向：
		// DEBIT Redis 已扣款 → 補償需加回 (RefundAndUnlock 內部用 HINCRBYFLOAT +amount)
		// CREDIT Redis 已加款 → 補償需扣回 (傳入負值)
		compensationAmount := amountFloat
		if compEvent.Type == domain.TxTypeWin || compEvent.Type == domain.TxTypeDeposit || compEvent.Type == domain.TxTypeRefund {
			compensationAmount = -amountFloat // 逆向補償
		}

		err := cache.RefundAndUnlock(context.Background(), compEvent.UserID, compEvent.Currency, compEvent.ProviderID, compEvent.ProviderTxID, compensationAmount)
		if err != nil {
			log.Printf("[Worker] ❌ Failed to revert Redis balance: %v", err)
			_ = d.Nack(false, true) // 重試
			return
		}

		log.Printf("[Worker] 🔧 Compensation done: Type=%s, User=%d, ProviderTxID=%s, Amount=%s",
			compEvent.Type, compEvent.UserID, compEvent.ProviderTxID, compEvent.Amount.String())
		_ = d.Ack(false)
		return
	}

	var event domain.TransactionCompletedEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		log.Printf("[Error] Failed to unmarshal message, discarding: %v", err)
		_ = d.Reject(false) // 格式錯誤，不要重試，直接丟掉或進 DLQ
		return
	}

	// [人為模擬錯誤]：如果金額是 999，我們模擬處理失敗，將訊息 Nack 且不重新入隊 (requeue=false)
	// 這樣 RabbitMQ 就會自動根據我們在宣告時綁定的設定，把它踢進 Dead Letter Queue (DLQ)
	if event.Amount.Equal(decimal.NewFromInt(999)) {
		log.Printf("[Worker] 🛑 Simulated Error: Rejecting transaction %s (Amount: 999), moving to DLQ...", event.TransactionID)
		_ = d.Nack(false, false)
		return
	}

	// 模擬正常的業務處理 (如：寫報表、算返水)
	log.Printf("[Worker] ✅ Successfully processed event: TxID=%s, UserID=%d, Amount=%s", event.TransactionID, event.UserID, event.Amount.String())
	_ = d.Ack(false)
}
