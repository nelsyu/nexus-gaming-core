package main

import (
	"os"

	nexushttp "github.com/bosstest/nexus-core/internal/delivery/http"
	"github.com/bosstest/nexus-core/internal/infrastructure/postgres"
	"github.com/bosstest/nexus-core/internal/infrastructure/rabbitmq"
	"github.com/bosstest/nexus-core/internal/infrastructure/redis"
	"github.com/bosstest/nexus-core/internal/usecase"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	// 1. 讀取環境變數
	if err := godotenv.Load(); err != nil {
		logger.GetLogger().Info("No .env file found, relying on environment variables")
	}

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		logger.GetLogger().Fatal("DB_DSN environment variable is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	sentinelAddrs := os.Getenv("REDIS_SENTINEL_ADDRS")
	if sentinelAddrs == "" {
		sentinelAddrs = "localhost:26379"
	}
	masterName := os.Getenv("REDIS_MASTER_NAME")
	if masterName == "" {
		masterName = "mymaster"
	}

	// 2. 初始化資料庫連線
	db, err := postgres.InitDB(dsn)
	if err != nil {
		logger.GetLogger().Fatal("Failed to initialize database", zap.Error(err))
	}
	defer db.Close()

	// 3. 初始化 Redis Sentinel 連線
	rdb := redis.InitSentinelClient(sentinelAddrs, masterName)
	defer rdb.Close()
	walletCache := redis.NewWalletCache(rdb)

	// 4. 初始化 RabbitMQ
	rabbitMQUrl := os.Getenv("RABBITMQ_URL")
	if rabbitMQUrl == "" {
		rabbitMQUrl = "amqp://guest:guest@localhost:5672/"
	}
	rmqClient, err := rabbitmq.InitRabbitMQ(rabbitMQUrl)
	if err != nil {
		logger.GetLogger().Fatal("Failed to initialize RabbitMQ", zap.Error(err))
	}
	defer rmqClient.Close()
	eventPublisher := rabbitmq.NewEventPublisher(rmqClient)

	// 5. 依賴注入 (Dependency Injection)
	// 初始化 Repositories
	walletRepo := postgres.NewWalletRepository(db)
	txRepo := postgres.NewTransactionRepository(db)
	uow := postgres.NewUnitOfWork(db)

	// 初始化 Usecase
	walletUsecase := usecase.NewWalletUsecase(uow, walletRepo, txRepo, walletCache, eventPublisher)

	// 初始化 Handler
	walletHandler := nexushttp.NewWalletHandler(walletUsecase)

	// 6. 註冊路由
	router := nexushttp.SetupRouter(walletHandler)

	// 7. 啟動伺服器
	logger.GetLogger().Info("Nexus-Core Server starting...", zap.String("port", port))
	if err := router.Run(":" + port); err != nil {
		logger.GetLogger().Fatal("Failed to run server", zap.Error(err))
	}
}
