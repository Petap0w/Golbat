package writer

import (
	"context"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack/v5"

	"golbat/db"
	"golbat/decoder"
	"golbat/pkg/queue"
)

type DBWriter struct {
	redis            *redis.Client
	db               *sqlx.DB
	consumerGroup    string
	consumerName     string
	batchSize        int64
	streams          []string
	trimTarget       int64 // Target size to trim streams to
	batchesProcessed int   // Counter for periodic trimming
}

func NewDBWriter(redis *redis.Client, db *sqlx.DB, consumerName string, batchSize int, trimTarget int64) *DBWriter {
	if batchSize == 0 {
		batchSize = 500
	}
	if trimTarget == 0 {
		trimTarget = 100000 // Default: keep 100k messages
	}

	return &DBWriter{
		redis:         redis,
		db:            db,
		consumerGroup: "golbat-writers",
		consumerName:  consumerName,
		batchSize:     int64(batchSize),
		trimTarget:    trimTarget,
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

		// No sleep needed - Block parameter in XReadGroup prevents tight loop
	}
}

func (w *DBWriter) processStream(ctx context.Context, stream string) error {
	// Auto-claim abandoned PENDING messages (from crashed workers)
	// Reclaim messages PENDING for >60 seconds (reduced frequency to avoid blocking Redis)
	// Use smaller count (100) to keep XAUTOCLAIM fast (<1s instead of 10-15s)
	claimedMsgs, _, err := w.redis.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    w.consumerGroup,
		Consumer: w.consumerName,
		MinIdle:  60 * time.Second, // Wait 60s instead of 10s
		Start:    "0-0",
		Count:    100, // Reduced from batchSize to avoid long-running XAUTOCLAIM
	}).Result()

	if err != nil && err != redis.Nil {
		log.Debugf("AutoClaim error on %s: %s", stream, err)
	}

	// Process reclaimed messages
	if len(claimedMsgs) > 0 {
		log.Infof("Reclaimed %d abandoned messages from %s", len(claimedMsgs), stream)
		if err := w.processBatch(ctx, stream, claimedMsgs); err != nil {
			log.Errorf("Failed to process reclaimed batch: %s", err)
		}
	}

	// Read new messages
	streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    w.consumerGroup,
		Consumer: w.consumerName,
		Streams:  []string{stream, ">"},
		Count:    w.batchSize,
		Block:    time.Millisecond, // Block for 1ms - allows batch accumulation without blocking
	}).Result()

	if err == redis.Nil {
		// No messages available
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

		// Periodically trim stream (every 100 batches to avoid blocking Redis)
		// With 75 workers, this means ~0.75 trims/sec instead of 7.5 trims/sec
		w.batchesProcessed++
		if w.batchesProcessed >= 100 {
			w.batchesProcessed = 0
			w.trimStream(ctx, stream)
		}
	}

	return nil
}

// trimStream keeps the stream at a manageable size by trimming old messages
// This is done in the background by workers, not during XADD
func (w *DBWriter) trimStream(ctx context.Context, stream string) {
	// Use XTRIM with MAXLEN ~ (approximate) for efficiency
	// This trims the stream to approximately trimTarget size
	trimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	deleted, err := w.redis.XTrimMaxLenApprox(trimCtx, stream, w.trimTarget, 0).Result()
	if err != nil {
		log.Warnf("Failed to trim %s: %s", stream, err)
		return
	}

	if deleted > 0 {
		log.Infof("Trimmed %d old messages from %s (target: %d)", deleted, stream, w.trimTarget)
	}
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

// isDeadlock checks if an error is a MySQL deadlock error
func isDeadlock(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Error 1213") || strings.Contains(err.Error(), "Deadlock")
}

// retryOnDeadlock retries a function on deadlock errors
func retryOnDeadlock(ctx context.Context, maxRetries int, fn func() error) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = fn()
		if err == nil {
			return nil
		}

		if !isDeadlock(err) {
			return err
		}

		// Exponential backoff on deadlock
		if i < maxRetries-1 {
			backoff := time.Duration(10*(i+1)) * time.Millisecond
			log.Debugf("Deadlock detected, retrying in %v (attempt %d/%d)", backoff, i+1, maxRetries)
			time.Sleep(backoff)
		}
	}
	return err
}

// Placeholder processor functions - will be implemented with actual batch logic

func (w *DBWriter) processPokestops(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	// Process each item individually to handle partial failures
	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var pokestop decoder.Pokestop
		if err := msgpack.Unmarshal(opData.Operation.Data, &pokestop); err != nil {
			log.Errorf("Failed to unmarshal pokestop: %s", err)
			// Don't ACK this message - it will be retried
			continue
		}

		// Try to upsert with retry on deadlock
		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertPokestops(ctx, w.db, []*decoder.Pokestop{&pokestop})
		})

		if err == nil {
			// Success - ACK this message
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			// Failed - don't ACK, will be retried
			log.Errorf("Failed to upsert pokestop %s: %s", pokestop.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d pokestops (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processGyms(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var gyms []*decoder.Gym
	for _, opData := range ops {
		var gym decoder.Gym
		if err := msgpack.Unmarshal(opData.Operation.Data, &gym); err != nil {
			log.Errorf("Failed to unmarshal gym: %s", err)
			continue
		}
		gyms = append(gyms, &gym)
	}

	if len(gyms) == 0 {
		return nil, nil
	}

	// Process individually to handle partial failures
	var successfulIds []string
	successCount := 0

	for i, gym := range gyms {
		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertGyms(ctx, w.db, []*decoder.Gym{gym})
		})

		if err == nil {
			successfulIds = append(successfulIds, ops[i].MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert gym %s: %s", gym.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d gyms (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processSpawnpoints(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var spawnpoint decoder.Spawnpoint
		if err := msgpack.Unmarshal(opData.Operation.Data, &spawnpoint); err != nil {
			log.Errorf("Failed to unmarshal spawnpoint: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertSpawnpoints(ctx, w.db, []*decoder.Spawnpoint{&spawnpoint})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert spawnpoint %d: %s", spawnpoint.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d spawnpoints (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processIncidents(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var incident decoder.Incident
		if err := msgpack.Unmarshal(opData.Operation.Data, &incident); err != nil {
			log.Errorf("Failed to unmarshal incident: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertIncidents(ctx, w.db, []*decoder.Incident{&incident})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert incident %s: %s", incident.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d incidents (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processTappables(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var tappable decoder.Tappable
		if err := msgpack.Unmarshal(opData.Operation.Data, &tappable); err != nil {
			log.Errorf("Failed to unmarshal tappable: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertTappables(ctx, w.db, []*decoder.Tappable{&tappable})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert tappable %d: %s", tappable.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d tappables (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processWeather(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var weather decoder.Weather
		if err := msgpack.Unmarshal(opData.Operation.Data, &weather); err != nil {
			log.Errorf("Failed to unmarshal weather: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertWeather(ctx, w.db, []*decoder.Weather{&weather})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert weather %d: %s", weather.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d weather records (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processStations(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var station decoder.Station
		if err := msgpack.Unmarshal(opData.Operation.Data, &station); err != nil {
			log.Errorf("Failed to unmarshal station: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertStations(ctx, w.db, []*decoder.Station{&station})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert station %s: %s", station.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d stations (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processRoutes(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var route decoder.Route
		if err := msgpack.Unmarshal(opData.Operation.Data, &route); err != nil {
			log.Errorf("Failed to unmarshal route: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertRoutes(ctx, w.db, []*decoder.Route{&route})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert route %s: %s", route.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d routes (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processS2Cells(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var cell decoder.S2Cell
		if err := msgpack.Unmarshal(opData.Operation.Data, &cell); err != nil {
			log.Errorf("Failed to unmarshal s2cell: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertS2Cells(ctx, w.db, []*decoder.S2Cell{&cell})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert s2cell %d: %s", cell.Id, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d s2cells (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}

func (w *DBWriter) processPlayers(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var successfulIds []string
	successCount := 0

	for _, opData := range ops {
		var player decoder.Player
		if err := msgpack.Unmarshal(opData.Operation.Data, &player); err != nil {
			log.Errorf("Failed to unmarshal player: %s", err)
			continue
		}

		err := retryOnDeadlock(ctx, 3, func() error {
			return db.BatchUpsertPlayers(ctx, w.db, []*decoder.Player{&player})
		})

		if err == nil {
			successfulIds = append(successfulIds, opData.MessageID)
			successCount++
		} else {
			log.Errorf("Failed to upsert player %s: %s", player.Name, err)
		}
	}

	if successCount > 0 {
		log.Infof("Processed batch of %d players (%d successful, %d failed)", len(ops), successCount, len(ops)-successCount)
	}

	return successfulIds, nil
}
