package queue

import (
	"context"
	"fmt"
	"sync"
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

// writeOp represents a single write operation queued for pipelining
type writeOp struct {
	stream  string
	opBytes []byte
}

type WriteQueue struct {
	client         *redis.Client
	batchBuffer    []writeOp
	batchMutex     sync.Mutex
	batchSize      int
	flushInterval  time.Duration
	flushTicker    *time.Ticker
	stopChan       chan struct{}
	flushesTotal   int64 // Metrics: total flushes
	writesTotal    int64 // Metrics: total writes queued
	lastMetricsLog time.Time
}

func NewWriteQueue(client *redis.Client, batchSize int, flushMs int) *WriteQueue {
	// Default values if not specified
	if batchSize <= 0 {
		batchSize = 500 // Default: flush every 500 writes
	}
	if flushMs <= 0 {
		flushMs = 25 // Default: flush every 25ms
	}

	q := &WriteQueue{
		client:         client,
		batchBuffer:    make([]writeOp, 0, batchSize),
		batchSize:      batchSize,
		flushInterval:  time.Duration(flushMs) * time.Millisecond,
		stopChan:       make(chan struct{}),
		lastMetricsLog: time.Now(),
	}

	// Start periodic flush ticker
	q.startPeriodicFlush()

	log.Infof("Redis pipeline initialized: batch_size=%d, flush_interval=%dms", batchSize, flushMs)

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

	// Add to batch buffer
	q.batchMutex.Lock()
	q.batchBuffer = append(q.batchBuffer, writeOp{
		stream:  stream,
		opBytes: opBytes,
	})
	q.writesTotal++
	shouldFlush := len(q.batchBuffer) >= q.batchSize
	q.batchMutex.Unlock()

	// Flush if batch is full (size-based flush)
	if shouldFlush {
		return q.flushBatch(ctx)
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

// flushBatch sends all queued writes to Redis in a single pipeline
func (q *WriteQueue) flushBatch(ctx context.Context) error {
	q.batchMutex.Lock()
	if len(q.batchBuffer) == 0 {
		q.batchMutex.Unlock()
		return nil
	}

	// Take ownership of current batch and reset buffer
	batch := q.batchBuffer
	q.batchBuffer = make([]writeOp, 0, q.batchSize)
	q.flushesTotal++
	q.batchMutex.Unlock()

	// Create pipeline and add all writes
	pipe := q.client.Pipeline()
	for _, write := range batch {
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: write.stream,
			Values: map[string]interface{}{
				"data": write.opBytes,
			},
		})
	}

	// Execute pipeline (single network round trip for all writes!)
	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Errorf("Pipeline flush failed for %d writes: %v", len(batch), err)
		return fmt.Errorf("pipeline flush failed: %w", err)
	}

	// Log metrics every 30 seconds
	if time.Since(q.lastMetricsLog) > 30*time.Second {
		q.batchMutex.Lock()
		avgBatchSize := float64(q.writesTotal) / float64(q.flushesTotal)
		log.Infof("Redis pipeline metrics: %d writes, %d flushes, avg %.1f writes/flush",
			q.writesTotal, q.flushesTotal, avgBatchSize)
		q.lastMetricsLog = time.Now()
		q.batchMutex.Unlock()
	}

	return nil
}

// startPeriodicFlush starts a background ticker that flushes the batch periodically
func (q *WriteQueue) startPeriodicFlush() {
	q.flushTicker = time.NewTicker(q.flushInterval)
	go func() {
		for {
			select {
			case <-q.flushTicker.C:
				// Time-based flush (ensures max latency = flushInterval)
				if err := q.flushBatch(context.Background()); err != nil {
					log.Debugf("Periodic flush error: %v", err)
				}
			case <-q.stopChan:
				q.flushTicker.Stop()
				return
			}
		}
	}()
}

// Stop stops the periodic flush ticker and flushes remaining writes
func (q *WriteQueue) Stop(ctx context.Context) error {
	log.Info("Stopping Redis pipeline...")
	close(q.stopChan)

	// Final flush of any remaining writes
	if err := q.flushBatch(ctx); err != nil {
		return err
	}

	log.Infof("Redis pipeline stopped. Final stats: %d writes, %d flushes", q.writesTotal, q.flushesTotal)
	return nil
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
