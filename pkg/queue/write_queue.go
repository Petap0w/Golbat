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
	client       *redis.Client
	maxQueueSize int64
}

func NewWriteQueue(client *redis.Client, maxQueueSize int64) *WriteQueue {
	if maxQueueSize == 0 {
		maxQueueSize = 1000000
	}

	return &WriteQueue{
		client:       client,
		maxQueueSize: maxQueueSize,
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

	// Add to stream
	err = q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: q.maxQueueSize,
		Approx: true,
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
		length, err := q.client.XLen(ctx, stream).Result()
		if err != nil {
			log.Warnf("Failed to get length of %s: %s", stream, err)
			continue
		}
		sizes[stream] = length
	}

	return sizes, nil
}

func (q *WriteQueue) Flush(ctx context.Context) error {
	// Wait for queues to drain (with timeout)
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			log.Warn("Queue flush timeout, some writes may be pending")
			return fmt.Errorf("flush timeout")
		case <-ticker.C:
			sizes, err := q.GetQueueSizes(ctx)
			if err != nil {
				return err
			}

			total := int64(0)
			for _, size := range sizes {
				total += size
			}

			if total == 0 {
				log.Info("All queues flushed successfully")
				return nil
			}

			log.Debugf("Waiting for queues to flush: %d items remaining", total)
		}
	}
}

func (q *WriteQueue) Close() error {
	// Queue doesn't need explicit close, Redis client handles it
	return nil
}

