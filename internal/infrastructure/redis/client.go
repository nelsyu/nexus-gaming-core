package redis

import (
	"context"
	"log"
	"strings"

	"github.com/redis/go-redis/v9"
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
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis Sentinel: %v", err)
	}

	return rdb
}
