package decoder

import (
	"context"
	
	"github.com/redis/go-redis/v9"
	
	"golbat/pkg/queue"
)

// Global Redis bridge components
var (
	writeQueue   *queue.WriteQueue
	redisClient  *redis.Client
	redisEnabled bool
)

// InitRedis initializes the Redis bridge components
func InitRedis(wq *queue.WriteQueue) {
	writeQueue = wq
	redisEnabled = wq != nil
}

// SetRedisClient sets the Redis client for fort cache updates
func SetRedisClient(client *redis.Client) {
	redisClient = client
}

// IsRedisEnabled returns whether Redis is enabled
func IsRedisEnabled() bool {
	return redisEnabled
}

// queueWrite queues a write operation
func queueWrite(ctx context.Context, writeType string, operation string, data interface{}) error {
	if writeQueue == nil {
		return nil
	}
	return writeQueue.QueueWrite(ctx, writeType, operation, data)
}

