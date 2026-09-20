package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bosstest/nexus-core/internal/domain"
	"github.com/redis/go-redis/v9"
)

const refreshTokenPrefix = "refresh_token:"

// RefreshTokenRepository 使用 Redis 保存 Opaque Refresh Token (以 SHA-256 雜湊儲存確保資料外洩防護)
type RefreshTokenRepository struct {
	rdb redis.UniversalClient
}

func NewRefreshTokenRepository(rdb redis.UniversalClient) *RefreshTokenRepository {
	return &RefreshTokenRepository{
		rdb: rdb,
	}
}

// hashToken 計算 Token 的 SHA-256 摘要，防止 Redis Dump 資料直接洩漏原始憑證
func (r *RefreshTokenRepository) hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// StoreRefreshToken 儲存 Refresh Token 雜湊與 UserID 對應，並設定生命週期 (TTL)
func (r *RefreshTokenRepository) StoreRefreshToken(ctx context.Context, token string, userID int64, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	key := fmt.Sprintf("%s%s", refreshTokenPrefix, r.hashToken(token))
	return r.rdb.Set(ctx, key, userID, ttl).Err()
}

// GetUserIDByRefreshToken 依據 Refresh Token 查找對應的 UserID
func (r *RefreshTokenRepository) GetUserIDByRefreshToken(ctx context.Context, token string) (int64, error) {
	key := fmt.Sprintf("%s%s", refreshTokenPrefix, r.hashToken(token))
	val, err := r.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, domain.ErrInvalidToken
		}
		return 0, err
	}

	userID, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("corrupted user id in refresh token store: %w", err)
	}

	return userID, nil
}

// RevokeRefreshToken 刪除指定的 Refresh Token，實作即時吊銷與 Token 輪換
func (r *RefreshTokenRepository) RevokeRefreshToken(ctx context.Context, token string) error {
	key := fmt.Sprintf("%s%s", refreshTokenPrefix, r.hashToken(token))
	return r.rdb.Del(ctx, key).Err()
}
