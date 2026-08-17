package main

import (
	"log"
	"os"

	nexushttp "github.com/bosstest/nexus-core/internal/delivery/http"
	"github.com/bosstest/nexus-core/internal/repository/postgres"
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

	// 2. 初始化資料庫連線
	db, err := postgres.InitDB(dsn)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// 3. 依賴注入 (Dependency Injection)
	// 初始化 Repositories
	walletRepo := postgres.NewWalletRepository(db)
	txRepo := postgres.NewTransactionRepository(db)

	// 初始化 Usecase
	walletUsecase := usecase.NewWalletUsecase(db, walletRepo, txRepo)

	// 初始化 Handler
	walletHandler := nexushttp.NewWalletHandler(walletUsecase)

	// 4. 註冊路由
	router := nexushttp.SetupRouter(walletHandler)

	// 5. 啟動伺服器
	log.Printf("Nexus-Core Server starting on port %s...", port)
	if err := router.Run(":" + port); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}
