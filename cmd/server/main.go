package main

import (
	"os"
	"strconv"
	"time"

	nexushttp "github.com/bosstest/nexus-core/internal/delivery/http"
	nexusauth "github.com/bosstest/nexus-core/internal/infrastructure/auth"
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

	// JWT 安全相關配置
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "nexus-gaming-core-enterprise-jwt-secret-key-change-in-prod"
		logger.GetLogger().Warn("JWT_SECRET not set, using default development secret key")
	}
	accessExpMin, _ := strconv.Atoi(os.Getenv("JWT_ACCESS_EXP_MINUTES"))
	if accessExpMin <= 0 {
		accessExpMin = 5
	}
	refreshExpHours, _ := strconv.Atoi(os.Getenv("JWT_REFRESH_EXP_HOURS"))
	if refreshExpHours <= 0 {
		refreshExpHours = 168
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
	tokenBlacklist := redis.NewTokenBlacklist(rdb)
	refreshTokenRepo := redis.NewRefreshTokenRepository(rdb)

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
	userRepo := postgres.NewUserRepository(db)
	uow := postgres.NewUnitOfWork(db)

	// 初始化安全基礎組件
	passwordHasher := nexusauth.NewBcryptHasher(10)
	jwtManager := nexusauth.NewJWTManager(
		jwtSecret,
		time.Duration(accessExpMin)*time.Minute,
		time.Duration(refreshExpHours)*time.Hour,
	)

	// 初始化 Usecases
	walletUsecase := usecase.NewWalletUsecase(uow, walletRepo, txRepo, walletCache, eventPublisher)
	authUsecase := usecase.NewAuthUsecase(uow, userRepo, walletRepo, passwordHasher, jwtManager, refreshTokenRepo, tokenBlacklist)

	// 初始化 Handlers
	walletHandler := nexushttp.NewWalletHandler(walletUsecase)
	authHandler := nexushttp.NewAuthHandler(authUsecase)

	// 6. 註冊路由與安全性中介軟體
	router := nexushttp.SetupRouter(walletHandler, authHandler, jwtManager, tokenBlacklist)

	// 7. 啟動伺服器
	logger.GetLogger().Info("Nexus-Core Server starting...", zap.String("port", port))
	if err := router.Run(":" + port); err != nil {
		logger.GetLogger().Fatal("Failed to run server", zap.Error(err))
	}
}
