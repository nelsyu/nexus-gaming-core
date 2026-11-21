package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/redis/go-redis/v9"
)

type nonceStore struct {
	client *redis.Client
}

// NewNonceStore 建立以 Redis 實作的 nonce 紀錄
func NewNonceStore(client *redis.Client) domain.NonceStore {
	return &nonceStore{client: client}
}

// MarkUsed 用 SET NX 原子地「檢查 + 標記」：只有第一個請求能寫入成功。
// TTL 只需涵蓋時間戳允許的窗口：超過窗口的舊請求會先被時間戳檢查擋下，不必永久保存 nonce。
func (s *nonceStore) MarkUsed(ctx context.Context, providerID, nonce string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf("provider_nonce:%s:%s", providerID, nonce)
	ok, err := s.client.SetNX(ctx, key, 1, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis mark nonce failed: %w", err)
	}
	return ok, nil
}
