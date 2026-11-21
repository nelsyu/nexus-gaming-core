package domain

import (
	"context"
	"errors"
	"time"
)

// GameProvider 遊戲商：以 HMAC 簽章呼叫錢包 API 的外部系統
type GameProvider struct {
	ProviderID string `db:"provider_id"`
	Name       string `db:"name"`
	APISecret  string `db:"api_secret"`
	AllowedIPs string `db:"allowed_ips"` // 逗號分隔的 IP / CIDR；空字串代表全部拒絕
	Enabled    bool   `db:"enabled"`
}

// ProviderRepository 讀取遊戲商資料
type ProviderRepository interface {
	// GetByID 找不到時回傳 ErrProviderNotFound
	GetByID(ctx context.Context, providerID string) (*GameProvider, error)
}

// NonceStore 記錄用過的 nonce，防止請求被重放
type NonceStore interface {
	// MarkUsed 標記 nonce 已使用。第一次使用回傳 true；已用過回傳 false。
	MarkUsed(ctx context.Context, providerID, nonce string, ttl time.Duration) (bool, error)
}

// ProviderAuthenticator 驗證遊戲商請求 (身分、來源 IP、時間戳、簽章、重放)
type ProviderAuthenticator interface {
	Authenticate(ctx context.Context, req ProviderAuthRequest) (*GameProvider, error)
}

// ProviderAuthRequest 驗證一個遊戲商請求所需的資料
type ProviderAuthRequest struct {
	ProviderID string
	Timestamp  string
	Nonce      string
	Signature  string
	Method     string
	Path       string
	Body       []byte
	ClientIP   string
}

var (
	ErrProviderNotFound = errors.New("provider not found")

	// ErrProviderUnauthorized 身分驗證失敗 (401)。
	// 對外一律回傳同一個訊息，不透露是「遊戲商不存在」還是「簽章錯誤」，避免給攻擊者線索；
	// 詳細原因以 %w 包裝在錯誤中，只寫進伺服器日誌。
	ErrProviderUnauthorized = errors.New("provider authentication failed")

	// ErrProviderIPNotAllowed 來源 IP 不在白名單 (403)
	ErrProviderIPNotAllowed = errors.New("source IP not allowed for provider")
)
