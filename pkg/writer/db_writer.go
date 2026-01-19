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
		consumerGroup: "golbat-writers",
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

		// No sleep needed - Block parameter in XReadGroup prevents tight loop
	}
}

func (w *DBWriter) processStream(ctx context.Context, stream string) error {
	streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    w.consumerGroup,
		Consumer: w.consumerName,
		Streams:  []string{stream, ">"},
		Count:    w.batchSize,
		Block:    50 * time.Millisecond, // Reduced from 1 second for faster response
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

	// Deserialize and batch write
	var pokestops []*decoder.Pokestop
	for _, opData := range ops {
		var pokestop decoder.Pokestop
		if err := msgpack.Unmarshal(opData.Operation.Data, &pokestop); err != nil {
			log.Errorf("Failed to unmarshal pokestop: %s", err)
			continue
		}
		pokestops = append(pokestops, &pokestop)
	}

	if len(pokestops) == 0 {
		return nil, nil
	}

	// Use batch insert with retry on deadlock
	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertPokestops(ctx, w.db, pokestops)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert pokestops after retries: %s", err)
		return nil, err
	}

	// Return message IDs for ACK
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d pokestops", len(pokestops))
	return ids, nil
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

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertGyms(ctx, w.db, gyms)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert gyms after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d gyms", len(gyms))
	return ids, nil
}

func (w *DBWriter) processSpawnpoints(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var spawnpoints []*decoder.Spawnpoint
	for _, opData := range ops {
		var spawnpoint decoder.Spawnpoint
		if err := msgpack.Unmarshal(opData.Operation.Data, &spawnpoint); err != nil {
			log.Errorf("Failed to unmarshal spawnpoint: %s", err)
			continue
		}
		spawnpoints = append(spawnpoints, &spawnpoint)
	}

	if len(spawnpoints) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertSpawnpoints(ctx, w.db, spawnpoints)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert spawnpoints after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d spawnpoints", len(spawnpoints))
	return ids, nil
}

func (w *DBWriter) processIncidents(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var incidents []*decoder.Incident
	for _, opData := range ops {
		var incident decoder.Incident
		if err := msgpack.Unmarshal(opData.Operation.Data, &incident); err != nil {
			log.Errorf("Failed to unmarshal incident: %s", err)
			continue
		}
		incidents = append(incidents, &incident)
	}

	if len(incidents) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertIncidents(ctx, w.db, incidents)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert incidents after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d incidents", len(incidents))
	return ids, nil
}

func (w *DBWriter) processTappables(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var tappables []*decoder.Tappable
	for _, opData := range ops {
		var tappable decoder.Tappable
		if err := msgpack.Unmarshal(opData.Operation.Data, &tappable); err != nil {
			log.Errorf("Failed to unmarshal tappable: %s", err)
			continue
		}
		tappables = append(tappables, &tappable)
	}

	if len(tappables) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertTappables(ctx, w.db, tappables)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert tappables after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d tappables", len(tappables))
	return ids, nil
}

func (w *DBWriter) processWeather(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var weather []*decoder.Weather
	for _, opData := range ops {
		var w decoder.Weather
		if err := msgpack.Unmarshal(opData.Operation.Data, &w); err != nil {
			log.Errorf("Failed to unmarshal weather: %s", err)
			continue
		}
		weather = append(weather, &w)
	}

	if len(weather) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertWeather(ctx, w.db, weather)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert weather after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d weather records", len(weather))
	return ids, nil
}

func (w *DBWriter) processStations(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var stations []*decoder.Station
	for _, opData := range ops {
		var station decoder.Station
		if err := msgpack.Unmarshal(opData.Operation.Data, &station); err != nil {
			log.Errorf("Failed to unmarshal station: %s", err)
			continue
		}
		stations = append(stations, &station)
	}

	if len(stations) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertStations(ctx, w.db, stations)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert stations after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d stations", len(stations))
	return ids, nil
}

func (w *DBWriter) processRoutes(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var routes []*decoder.Route
	for _, opData := range ops {
		var route decoder.Route
		if err := msgpack.Unmarshal(opData.Operation.Data, &route); err != nil {
			log.Errorf("Failed to unmarshal route: %s", err)
			continue
		}
		routes = append(routes, &route)
	}

	if len(routes) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertRoutes(ctx, w.db, routes)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert routes after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d routes", len(routes))
	return ids, nil
}

func (w *DBWriter) processS2Cells(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var cells []*decoder.S2Cell
	for _, opData := range ops {
		var cell decoder.S2Cell
		if err := msgpack.Unmarshal(opData.Operation.Data, &cell); err != nil {
			log.Errorf("Failed to unmarshal s2cell: %s", err)
			continue
		}
		cells = append(cells, &cell)
	}

	if len(cells) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertS2Cells(ctx, w.db, cells)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert s2cells after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d s2cells", len(cells))
	return ids, nil
}

func (w *DBWriter) processPlayers(ctx context.Context, ops []OperationData) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	var players []*decoder.Player
	for _, opData := range ops {
		var player decoder.Player
		if err := msgpack.Unmarshal(opData.Operation.Data, &player); err != nil {
			log.Errorf("Failed to unmarshal player: %s", err)
			continue
		}
		players = append(players, &player)
	}

	if len(players) == 0 {
		return nil, nil
	}

	err := retryOnDeadlock(ctx, 3, func() error {
		return db.BatchUpsertPlayers(ctx, w.db, players)
	})
	if err != nil {
		log.Errorf("Failed to batch upsert players after retries: %s", err)
		return nil, err
	}

	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.MessageID
	}

	log.Infof("Processed batch of %d players", len(players))
	return ids, nil
}
