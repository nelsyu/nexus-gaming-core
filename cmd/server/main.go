package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	nexushttp "github.com/bosstest/nexus-core/internal/delivery/http"
	customvalidator "github.com/bosstest/nexus-core/internal/delivery/http/validator"
	nexusauth "github.com/bosstest/nexus-core/internal/infrastructure/auth"
	"github.com/bosstest/nexus-core/internal/infrastructure/postgres"
	"github.com/bosstest/nexus-core/internal/infrastructure/rabbitmq"
	"github.com/bosstest/nexus-core/internal/infrastructure/redis"
	"github.com/bosstest/nexus-core/internal/usecase"
	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

// shutdownTimeout 優雅關機的最長等待時間，需小於 docker-compose 的 stop_grace_period
const shutdownTimeout = 20 * time.Second

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

	// 6. 註冊自定義驗證器並配置 Gin 使用 validate tag
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		// 將 Gin 的 tag 從 "binding" 改為 "validate"
		v.SetTagName("validate")

		// 註冊自定義驗證器
		if err := customvalidator.RegisterCustomValidators(v); err != nil {
			logger.GetLogger().Fatal("Failed to register custom validators", zap.Error(err))
		}
		logger.GetLogger().Info("Custom validators registered successfully with 'validate' tag")
	}

	// 7. 註冊路由與安全性中介軟體
	router := nexushttp.SetupRouter(walletHandler, authHandler, jwtManager, tokenBlacklist)

	// 8. 啟動伺服器
	// 使用 http.Server 而非 router.Run，才能設定逾時並支援優雅關機
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,  // 防 Slowloris：限制讀取 Header 的時間
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second, // 需大於單筆交易最長處理時間 (含 Hydration 等待約 2.5 秒)
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.GetLogger().Info("Nexus-Core Server starting...", zap.String("port", port))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.GetLogger().Fatal("Failed to run server", zap.Error(err))
		}
	}()

	// 9. 優雅關機：收到 SIGTERM (例如部署/重啟) 時，停止接新請求，等進行中的交易完成再關閉。
	// 若直接被砍掉，交易可能停在「Redis 已扣、DB 未寫」的中間狀態。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.GetLogger().Info("Shutting down server gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.GetLogger().Error("Server forced to shutdown", zap.Error(err))
	}
	// main 返回後，上面註冊的 defer 依序關閉 RabbitMQ、Redis、DB 連線
	logger.GetLogger().Info("Server exited")
}
