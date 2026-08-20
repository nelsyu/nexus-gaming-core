package main

import (
	"log"
	"os"

	nexushttp "github.com/bosstest/nexus-core/internal/delivery/http"
	"github.com/bosstest/nexus-core/internal/repository/postgres"
	"github.com/bosstest/nexus-core/internal/repository/redis"
	"github.com/bosstest/nexus-core/internal/usecase"
	"github.com/joho/godotenv"
)

func main() {
	// 1. 讀取環境變數
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on environment variables")
	}

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Fatal("DB_DSN environment variable is required")
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
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// 3. 初始化 Redis Sentinel 連線
	rdb := redis.InitSentinelClient(sentinelAddrs, masterName)
	defer rdb.Close()
	walletCache := redis.NewWalletCache(rdb)

	// 4. 依賴注入 (Dependency Injection)
	// 初始化 Repositories
	walletRepo := postgres.NewWalletRepository(db)
	txRepo := postgres.NewTransactionRepository(db)

	// 初始化 Usecase
	walletUsecase := usecase.NewWalletUsecase(db, walletRepo, txRepo, walletCache)

	// 初始化 Handler
	walletHandler := nexushttp.NewWalletHandler(walletUsecase)

	// 5. 註冊路由
	router := nexushttp.SetupRouter(walletHandler)

	// 6. 啟動伺服器
	log.Printf("Nexus-Core Server starting on port %s...", port)
	if err := router.Run(":" + port); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}
