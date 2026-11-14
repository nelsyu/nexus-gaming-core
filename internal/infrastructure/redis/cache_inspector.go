package redis

import (
	"context"
	"errors"
	"fmt"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/redis/go-redis/v9"
)

type cacheInspector struct {
	client *redis.Client
}

// NewCacheInspector 建立對帳用的快取檢查器
func NewCacheInspector(client *redis.Client) domain.CacheInspector {
	return &cacheInspector{client: client}
}

// GetCachedBalances 用 Pipeline 一次送出多個 HGET，避免每個錢包都來回一次網路。
func (c *cacheInspector) GetCachedBalances(ctx context.Context, wallets []domain.WalletSnapshot) (map[int64]int64, error) {
	pipe := c.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(wallets))
	for i, w := range wallets {
		cmds[i] = pipe.HGet(ctx, walletKey(w.UserID, w.Currency), "balance")
	}
	// 快取不存在時 HGET 回傳 redis.Nil，Exec 會把它當成錯誤回傳，這是正常情況
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("redis pipeline exec failed: %w", err)
	}

	balances := make(map[int64]int64, len(wallets))
	for i, cmd := range cmds {
		units, err := cmd.Int64()
		if errors.Is(err, redis.Nil) {
			continue // 沒有快取，沒得比對
		}
		if err != nil {
			return nil, fmt.Errorf("parse cached balance of wallet %d: %w", wallets[i].ID, err)
		}
		balances[wallets[i].ID] = units
	}
	return balances, nil
}

// InvalidateWallet 只刪除錢包快取 (不動交易防重鎖)
func (c *cacheInspector) InvalidateWallet(ctx context.Context, userID int64, currency string) error {
	if err := c.client.Del(ctx, walletKey(userID, currency)).Err(); err != nil {
		return fmt.Errorf("redis invalidate wallet failed: %w", err)
	}
	return nil
}
