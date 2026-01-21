package decoder

import (
	"context"
	
	"github.com/redis/go-redis/v9"
	
	"golbat/pkg/cache"
	"golbat/pkg/queue"
)

// Global Redis bridge components
var (
	l2Cache         *cache.L2Cache
	writeQueue      *queue.WriteQueue
	spawnpointBatch *cache.SpawnpointLoader
	redisClient     *redis.Client
	redisEnabled    bool
)

// InitRedis initializes the Redis bridge components
func InitRedis(l2 *cache.L2Cache, wq *queue.WriteQueue, spBatch *cache.SpawnpointLoader) {
	l2Cache = l2
	writeQueue = wq
	spawnpointBatch = spBatch
	redisEnabled = l2 != nil && wq != nil
}

// SetRedisClient sets the Redis client for fort cache updates
func SetRedisClient(client *redis.Client) {
	redisClient = client
}

// IsRedisEnabled returns whether Redis is enabled
func IsRedisEnabled() bool {
	return redisEnabled
}

// getFromL2Cache attempts to get a value from L2 cache
func getFromL2Cache(ctx context.Context, key string, dest interface{}) error {
	if l2Cache == nil {
		return cache.ErrCacheMiss
	}
	return l2Cache.Get(ctx, key, dest)
}

// setToL2Cache sets a value in L2 cache
func setToL2Cache(ctx context.Context, key string, value interface{}) error {
	if l2Cache == nil {
		return nil
	}
	return l2Cache.Set(ctx, key, value)
}

// queueWrite queues a write operation
func queueWrite(ctx context.Context, writeType string, operation string, data interface{}) error {
	if writeQueue == nil {
		return nil
	}
	return writeQueue.QueueWrite(ctx, writeType, operation, data)
}

