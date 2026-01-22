package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	WriteTypePokestop   = "pokestop"
	WriteTypeGym        = "gym"
	WriteTypeSpawnpoint = "spawnpoint"
	WriteTypeIncident   = "incident"
	WriteTypeTappable   = "tappable"
	WriteTypeWeather    = "weather"
	WriteTypeStation    = "station"
	WriteTypeRoute      = "route"
	WriteTypeS2Cell     = "s2cell"
	WriteTypePlayer     = "player"
)

const (
	StreamCritical = "golbat_writes:critical"
	StreamHigh     = "golbat_writes:high"
	StreamNormal   = "golbat_writes:normal"
)

type WriteOperation struct {
	Type      string `msgpack:"type"`
	Operation string `msgpack:"operation"` // "upsert", "delete"
	Data      []byte `msgpack:"data"`
	Timestamp int64  `msgpack:"timestamp"`
}

type WriteQueue struct {
	client *redis.Client
}

func NewWriteQueue(client *redis.Client) *WriteQueue {
	return &WriteQueue{
		client: client,
	}
}

func (q *WriteQueue) QueueWrite(ctx context.Context, writeType string, operation string, data interface{}) error {
	// Serialize data
	dataBytes, err := msgpack.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	op := WriteOperation{
		Type:      writeType,
		Operation: operation,
		Data:      dataBytes,
		Timestamp: time.Now().Unix(),
	}

	opBytes, err := msgpack.Marshal(op)
	if err != nil {
		return fmt.Errorf("failed to marshal operation: %w", err)
	}

	stream := q.getStreamForType(writeType)

	// Add to stream (no MAXLEN - workers handle cleanup)
	err = q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: map[string]interface{}{
			"data": opBytes,
		},
	}).Err()

	if err != nil {
		return fmt.Errorf("failed to add to stream: %w", err)
	}

	return nil
}

func (q *WriteQueue) getStreamForType(writeType string) string {
	switch writeType {
	case WriteTypePokestop, WriteTypeGym, WriteTypeSpawnpoint:
		return StreamCritical
	case WriteTypeIncident, WriteTypeTappable, WriteTypeWeather:
		return StreamHigh
	default:
		return StreamNormal
	}
}

func (q *WriteQueue) GetQueueSizes(ctx context.Context) (map[string]int64, error) {
	sizes := make(map[string]int64)

	streams := []string{StreamCritical, StreamHigh, StreamNormal}
	for _, stream := range streams {
		// Get lag (unprocessed messages) instead of XLEN (total messages)
		// XLEN includes processed messages, lag shows actual backlog
		groups, err := q.client.XInfoGroups(ctx, stream).Result()
		if err != nil {
			log.Warnf("Failed to get group info for %s: %s", stream, err)
			continue
		}

		// Find golbat-writers group and get lag
		for _, group := range groups {
			if group.Name == "golbat-writers" {
				sizes[stream] = group.Lag
				break
			}
		}
	}

	return sizes, nil
}

func (q *WriteQueue) Flush(ctx context.Context) error {
	// Create a new context with timeout for flush operation
	// Don't use the parent context which might already be canceled
	flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-flushCtx.Done():
			// Check final state before giving up
			sizes, _ := q.GetQueueSizes(context.Background())
			total := int64(0)
			for _, size := range sizes {
				total += size
			}
			if total > 0 {
				log.Warnf("Queue flush timeout, %d writes may be pending", total)
				return fmt.Errorf("flush timeout: %d items remaining", total)
			}
			log.Info("All queues flushed successfully")
			return nil

		case <-ticker.C:
			sizes, err := q.GetQueueSizes(flushCtx)
			if err != nil {
				log.Warnf("Failed to check queue sizes during flush: %s", err)
				continue
			}

			total := int64(0)
			for _, size := range sizes {
				total += size
			}

			if total == 0 {
				log.Info("All queues flushed successfully")
				return nil
			}

			log.Infof("Waiting for queues to flush: %d items remaining", total)
		}
	}
}

func (q *WriteQueue) Close() error {
	// Queue doesn't need explicit close, Redis client handles it
	return nil
}
