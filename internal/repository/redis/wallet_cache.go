package redis

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed pre_deduct.lua
var preDeductScript string

//go:embed refund_and_unlock.lua
var refundUnlockScript string

type WalletCache struct {
	client       *redis.Client
	preDeduct    *redis.Script
	refundUnlock *redis.Script
}

// NewWalletCache 建立新的 WalletCache 實例
func NewWalletCache(client *redis.Client) *WalletCache {
	return &WalletCache{
		client:       client,
		preDeduct:    redis.NewScript(preDeductScript),
		refundUnlock: redis.NewScript(refundUnlockScript),
	}
}

// 預扣款結果常數
const (
	ResultSuccess      = 0
	ResultDuplicateTx  = 1
	ResultInsufficient = 2
	ResultCacheMiss    = 3
)

// PreDeduct 執行原子化的 Lua 預扣款
func (c *WalletCache) PreDeduct(ctx context.Context, providerID, providerTxID string, userID int64, currency string, amount float64) (int, error) {
	txLockKey := fmt.Sprintf("tx:%s:%s", providerID, providerTxID)
	walletKey := fmt.Sprintf("wallet:%d:%s", userID, currency)

	keys := []string{txLockKey, walletKey}
	args := []any{"BET", amount}

	result, err := c.preDeduct.Run(ctx, c.client, keys, args...).Int()
	if err != nil {
		return -1, fmt.Errorf("redis script run failed: %w", err)
	}

	return result, nil
}

// InitCacheIfMissing 將 DB 中的餘額寫入 Redis。
// 使用 HSETNX 確保「只有在 Redis 完全沒有該 key 時才寫入」，
// 避免覆寫其他並發進行中的 HINCRBYFLOAT 相對增減。
func (c *WalletCache) InitCacheIfMissing(ctx context.Context, userID int64, currency string, balance float64) error {
	walletKey := fmt.Sprintf("wallet:%d:%s", userID, currency)
	success, err := c.client.HSetNX(ctx, walletKey, "balance", balance).Result()
	if err == nil && success {
		// 只有真正寫入成功，才設定過期時間
		c.client.Expire(ctx, walletKey, 1*time.Hour)
	}
	return err
}

// RefundAndUnlock 執行原子化的退款並移除防重鎖
func (c *WalletCache) RefundAndUnlock(ctx context.Context, userID int64, currency, providerID, providerTxID string, amount float64) error {
	txLockKey := fmt.Sprintf("tx:%s:%s", providerID, providerTxID)
	walletKey := fmt.Sprintf("wallet:%d:%s", userID, currency)

	keys := []string{txLockKey, walletKey}
	args := []any{amount}

	err := c.refundUnlock.Run(ctx, c.client, keys, args...).Err()
	if err != nil {
		return fmt.Errorf("redis refund script run failed: %w", err)
	}

	return nil
}

