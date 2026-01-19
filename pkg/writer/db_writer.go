package writer

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack/v5"

	"golbat/pkg/queue"
)

type DBWriter struct {
	redis         *redis.Client
	db            *sqlx.DB
	consumerGroup string
	consumerName  string
	batchSize     int64
	streams       []string
}

func NewDBWriter(redis *redis.Client, db *sqlx.DB, consumerName string, batchSize int) *DBWriter {
	if batchSize == 0 {
		batchSize = 500
	}

	return &DBWriter{
		redis:         redis,
		db:            db,
		consumerGroup: "db_writers",
		consumerName:  consumerName,
		batchSize:     int64(batchSize),
		streams: []string{
			queue.StreamCritical,
			queue.StreamHigh,
			queue.StreamNormal,
		},
	}
}

func (w *DBWriter) Run(ctx context.Context) error {
	log.Infof("DB Writer %s starting...", w.consumerName)

	// Create consumer groups if they don't exist
	for _, stream := range w.streams {
		err := w.redis.XGroupCreateMkStream(ctx, stream, w.consumerGroup, "0").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			log.Warnf("Failed to create consumer group for %s: %s", stream, err)
		}
	}

	// Process each stream
	for {
		select {
		case <-ctx.Done():
			log.Infof("DB Writer %s shutting down...", w.consumerName)
			return nil
		default:
		}

		// Read from all streams (prioritize critical)
		for _, stream := range w.streams {
			if err := w.processStream(ctx, stream); err != nil {
				log.Errorf("Error processing stream %s: %s", stream, err)
			}
		}

		// Small delay to prevent tight loop
		time.Sleep(100 * time.Millisecond)
	}
}

func (w *DBWriter) processStream(ctx context.Context, stream string) error {
	streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    w.consumerGroup,
		Consumer: w.consumerName,
		Streams:  []string{stream, ">"},
		Count:    w.batchSize,
		Block:    time.Second,
	}).Result()

	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}

	if len(streams) == 0 {
		return nil
	}

	for _, stream := range streams {
		if err := w.processBatch(ctx, stream.Stream, stream.Messages); err != nil {
			log.Errorf("Failed to process batch from %s: %s", stream.Stream, err)
			return err
		}
	}

	return nil
}

func (w *DBWriter) processBatch(ctx context.Context, stream string, messages []redis.XMessage) error {
	if len(messages) == 0 {
		return nil
	}

	// Group operations by type
	operations := make(map[string][]OperationData)

	for _, msg := range messages {
		dataBytes, ok := msg.Values["data"].(string)
		if !ok {
			log.Warnf("Invalid message format in %s: %v", stream, msg.ID)
			continue
		}

		var op queue.WriteOperation
		if err := msgpack.Unmarshal([]byte(dataBytes), &op); err != nil {
			log.Errorf("Failed to unmarshal operation: %s", err)
			continue
		}

		operations[op.Type] = append(operations[op.Type], OperationData{
			Operation: op,
			MessageID: msg.ID,
		})
	}

	// Process each type
	var processedIDs []string
	for opType, ops := range operations {
		ids, err := w.processOperationType(ctx, opType, ops)
		if err != nil {
			log.Errorf("Failed to process %s operations: %s", opType, err)
			// Don't ACK if processing failed
			continue
		}
		processedIDs = append(processedIDs, ids...)
	}

	// ACK processed messages
	if len(processedIDs) > 0 {
		if err := w.redis.XAck(ctx, stream, w.consumerGroup, processedIDs...).Err(); err != nil {
			log.Errorf("Failed to ACK messages: %s", err)
		}

		log.Debugf("Processed %d operations from %s", len(processedIDs), stream)
	}

	return nil
}

func (w *DBWriter) processOperationType(ctx context.Context, opType string, ops []OperationData) ([]string, error) {
	switch opType {
	case queue.WriteTypePokestop:
		return w.processPokestops(ctx, ops)
	case queue.WriteTypeGym:
		return w.processGyms(ctx, ops)
	case queue.WriteTypeSpawnpoint:
		return w.processSpawnpoints(ctx, ops)
	case queue.WriteTypeIncident:
		return w.processIncidents(ctx, ops)
	case queue.WriteTypeTappable:
		return w.processTappables(ctx, ops)
	case queue.WriteTypeWeather:
		return w.processWeather(ctx, ops)
	case queue.WriteTypeStation:
		return w.processStations(ctx, ops)
	case queue.WriteTypeRoute:
		return w.processRoutes(ctx, ops)
	case queue.WriteTypeS2Cell:
		return w.processS2Cells(ctx, ops)
	case queue.WriteTypePlayer:
		return w.processPlayers(ctx, ops)
	default:
		log.Warnf("Unknown operation type: %s", opType)
		// Return IDs to ACK even if we don't know how to process
		ids := make([]string, len(ops))
		for i, op := range ops {
			ids[i] = op.MessageID
		}
		return ids, nil
	}
}

type OperationData struct {
	Operation queue.WriteOperation
	MessageID string
}

// Placeholder processor functions - will be implemented with actual batch logic

func (w *DBWriter) processPokestops(ctx context.Context, ops []OperationData) ([]string, error) {
	// TODO: Implement batch pokestop processing
	log.Debugf("Processing %d pokestop operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processGyms(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d gym operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processSpawnpoints(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d spawnpoint operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processIncidents(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d incident operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processTappables(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d tappable operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processWeather(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d weather operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processStations(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d station operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processRoutes(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d route operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processS2Cells(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d s2cell operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

func (w *DBWriter) processPlayers(ctx context.Context, ops []OperationData) ([]string, error) {
	log.Debugf("Processing %d player operations", len(ops))
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}
	return ids, nil
}

