package redis

import (
	"context"
	"strings"

	"github.com/bosstest/nexus-core/pkg/logger"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// InitSentinelClient 初始化並回傳一個 Redis Sentinel 連線池
func InitSentinelClient(sentinelAddrs string, masterName string) *redis.Client {
	addrs := strings.Split(sentinelAddrs, ",")

	rdb := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    masterName,
		SentinelAddrs: addrs,
		// Password: "", // 本地測試使用 ALLOW_EMPTY_PASSWORD=yes
		DB: 0,
	})

	// 測試連線
	ctx := context.Background()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		logger.GetLogger().Fatal("Failed to connect to Redis Sentinel", zap.Error(err))
	}

	return rdb
}
