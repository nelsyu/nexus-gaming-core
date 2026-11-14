package redis

import (
	"context"
	_ "embed"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/redis/go-redis/v9"
)

//go:embed pre_process.lua
var preProcessScript string

const (
	// walletCacheBaseTTL 錢包快取基礎存活時間
	walletCacheBaseTTL = 1 * time.Hour
	// walletCacheTTLJitter 隨機抖動上限，避免大量快取在同一秒一起過期造成 DB 被擊穿
	walletCacheTTLJitter = 10 * time.Minute
)

// walletKey 錢包快取 key。
// v2：餘額改為整數最小單位。換前綴可避免部署時讀到舊版的浮點數字串 (HINCRBY 會報錯)，
// 舊 key 會依 TTL 自然過期。
func walletKey(userID int64, currency string) string {
	return fmt.Sprintf("wallet:v2:%d:%s", userID, currency)
}

func txLockKey(providerID, providerTxID string) string {
	return fmt.Sprintf("tx:%s:%s", providerID, providerTxID)
}

// WalletCache 實作了 domain.WalletCache 介面
type walletCache struct {
	client     *redis.Client
	preProcess *redis.Script
}

// NewWalletCache 建立新的 WalletCache 實例
func NewWalletCache(client *redis.Client) domain.WalletCache {
	return &walletCache{
		client:     client,
		preProcess: redis.NewScript(preProcessScript),
	}
}

// PreProcess 執行原子化的 Lua 預處理，統一支援扣款 (DEBIT) 與加款 (CREDIT)。
// op 使用 OpDebit 或 OpCredit 常數，amountUnits 為整數最小單位。
// - DEBIT：鎖定防重、檢查餘額、原子扣款。
// - CREDIT：鎖定防重（防止重複派彩）、原子加款，不做餘額下限檢查。
// Cache Miss 時會釋放防重鎖並回傳 ResultCacheMiss，由 Go 層負責重建快取。
func (c *walletCache) PreProcess(ctx context.Context, op, providerID, providerTxID string, userID int64, currency string, amountUnits int64) (int, error) {
	keys := []string{txLockKey(providerID, providerTxID), walletKey(userID, currency)}
	args := []any{op, amountUnits}

	result, err := c.preProcess.Run(ctx, c.client, keys, args...).Int()
	if err != nil {
		return -1, fmt.Errorf("redis pre-process script run failed: %w", err)
	}

	return result, nil
}

// InitCacheIfMissing 將 DB 中的餘額寫入 Redis。
// 使用 HSETNX 確保「只有在 Redis 完全沒有該 key 時才寫入」，
// 避免覆寫其他並發進行中的 HINCRBY 相對增減。
func (c *walletCache) InitCacheIfMissing(ctx context.Context, userID int64, currency string, balanceUnits int64) error {
	key := walletKey(userID, currency)
	success, err := c.client.HSetNX(ctx, key, "balance", balanceUnits).Result()
	if err == nil && success {
		// 只有真正寫入成功，才設定過期時間 (帶隨機抖動)
		ttl := walletCacheBaseTTL + rand.N(walletCacheTTLJitter)
		c.client.Expire(ctx, key, ttl)
	}
	return err
}

// Invalidate 刪除錢包快取與該筆交易的防重鎖。
//
// 為什麼連防重鎖一起刪？DB 沒落庫 (或不確定) 時，遊戲商用同一個 provider_tx_id 重送
// 必須能重新進來處理；若 DB 其實已經 commit，重送會被 DB 的 UNIQUE 約束擋下並回傳既有交易，
// 所以刪鎖是安全的。
func (c *walletCache) Invalidate(ctx context.Context, userID int64, currency, providerID, providerTxID string) error {
	err := c.client.Del(ctx, walletKey(userID, currency), txLockKey(providerID, providerTxID)).Err()
	if err != nil {
		return fmt.Errorf("redis invalidate failed: %w", err)
	}
	return nil
}

// AcquireHydrationLock 嘗試取得快取重建鎖，避免 Cache Stampede。
func (c *walletCache) AcquireHydrationLock(ctx context.Context, userID int64) (bool, error) {
	key := fmt.Sprintf("hydration_lock:%d", userID)
	// 5 秒 TTL 足夠應付一次資料庫讀取，也能防止 Worker 崩潰導致死鎖
	return c.client.SetNX(ctx, key, "1", 5*time.Second).Result()
}

// ReleaseHydrationLock 釋放快取重建鎖
func (c *walletCache) ReleaseHydrationLock(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("hydration_lock:%d", userID)
	return c.client.Del(ctx, key).Err()
}
