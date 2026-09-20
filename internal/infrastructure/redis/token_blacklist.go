package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const blacklistKeyPrefix = "blacklist:jti:"

// TokenBlacklist 使用 Redis 實作 Token 吊銷黑名單
type TokenBlacklist struct {
	rdb redis.UniversalClient
}

func NewTokenBlacklist(rdb redis.UniversalClient) *TokenBlacklist {
	return &TokenBlacklist{
		rdb: rdb,
	}
}

// RevokeToken 將 token 的 jti 加入黑名單，TTL 設定為該 token 的剩餘存活時間
func (b *TokenBlacklist) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil // 已經過期的 token 不需要再佔用快取空間
	}
	key := fmt.Sprintf("%s%s", blacklistKeyPrefix, jti)
	return b.rdb.Set(ctx, key, "1", ttl).Err()
}

// IsRevoked 檢查指定的 jti 是否存在於黑名單中 (O(1) 複雜度)
func (b *TokenBlacklist) IsRevoked(ctx context.Context, jti string) (bool, error) {
	key := fmt.Sprintf("%s%s", blacklistKeyPrefix, jti)
	exists, err := b.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}
