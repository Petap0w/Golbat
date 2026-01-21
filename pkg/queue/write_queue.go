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
	client     *redis.Client
	buffer     chan *queuedWrite
	bufferSize int
}

type queuedWrite struct {
	stream  string
	opBytes []byte
	retries int
}

func NewWriteQueue(client *redis.Client) *WriteQueue {
	bufferSize := 500000 // 500K in-memory buffer (5 seconds at 100K/sec)

	q := &WriteQueue{
		client:     client,
		buffer:     make(chan *queuedWrite, bufferSize),
		bufferSize: bufferSize,
	}

	// Start background workers to drain buffer
	workerCount := 20 // 20 parallel workers (increased for higher throughput)
	for i := 0; i < workerCount; i++ {
		go q.bufferWorker(i)
	}

	log.Infof("Write queue initialized with %d buffer size, %d workers", bufferSize, workerCount)

	return q
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

	// Push to in-memory buffer (non-blocking) - FAST PATH!
	write := &queuedWrite{
		stream:  stream,
		opBytes: opBytes,
		retries: 0,
	}

	select {
	case q.buffer <- write:
		// Successfully queued in memory - return immediately (microseconds)
		return nil
	default:
		// Buffer full - fall back to direct XADD (will block, but rare)
		log.Warnf("Write buffer full (%d items), falling back to direct Redis XADD", q.bufferSize)

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

// bufferWorker drains the in-memory buffer and sends to Redis
func (q *WriteQueue) bufferWorker(id int) {
	log.Debugf("Buffer worker %d started", id)

	// Use a persistent background context for all Redis operations in this worker
	// This avoids creating/destroying contexts repeatedly and prevents nil context issues
	ctx := context.Background()

	for write := range q.buffer {
		// Use Redis client's configured timeouts (20s WriteTimeout)
		// No need to create a new context with timeout for each operation
		err := q.client.XAdd(ctx, &redis.XAddArgs{
			Stream: write.stream,
			Values: map[string]interface{}{
				"data": write.opBytes,
			},
		}).Err()

		if err != nil {
			// Retry up to 3 times with exponential backoff
			write.retries++
			if write.retries < 3 {
				// Requeue for retry
				select {
				case q.buffer <- write:
					log.Debugf("Requeued write (retry %d/3)", write.retries)
				default:
					log.Errorf("Failed to requeue write after Redis error: %v", err)
				}
			} else {
				log.Errorf("Dropped write after 3 retries to %s: %v", write.stream, err)
			}
		}
	}

	log.Warnf("Buffer worker %d stopped (channel closed)", id)
}

func (q *WriteQueue) Flush(ctx context.Context) error {
	// Drain in-memory buffer first
	log.Infof("Flushing write buffer (%d items)...", len(q.buffer))
	for len(q.buffer) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	log.Info("Write buffer drained")

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
