package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"
)

// SpawnpointLoader handles efficient batch loading of spawnpoints from L2 and DB
// L1 cache is handled separately in the decoder package to avoid circular dependencies
type SpawnpointLoader struct {
	l2Cache *L2Cache
	db      *sqlx.DB
	mu      sync.Mutex
}

type SpawnpointRecord struct {
	Id         int64   `db:"id"`
	Lat        float64 `db:"lat"`
	Lon        float64 `db:"lon"`
	DespawnSec *int64  `db:"despawn_sec"` // Nullable
	Updated    int64   `db:"updated"`
	LastSeen   int64   `db:"last_seen"`
}

func NewSpawnpointLoader(l2Cache *L2Cache, db *sqlx.DB) *SpawnpointLoader {
	return &SpawnpointLoader{
		l2Cache: l2Cache,
		db:      db,
	}
}

// BatchLoad loads multiple spawnpoints in one operation, checking L2 -> DB
// L1 cache checking happens in the decoder package
func (s *SpawnpointLoader) BatchLoad(ctx context.Context, ids []int64) (map[int64]*SpawnpointRecord, error) {
	if len(ids) == 0 {
		return make(map[int64]*SpawnpointRecord), nil
	}

	result := make(map[int64]*SpawnpointRecord)

	// First pass: Batch check L2 (Redis)
	var missingFromL2 []int64
	if s.l2Cache != nil {
		l2Data, err := s.l2Cache.BatchGetSpawnpoints(ctx, ids)
		if err != nil {
			log.Warnf("L2 batch get failed: %s", err)
			missingFromL2 = ids
		} else {
			for _, id := range ids {
				if data, found := l2Data[id]; found {
					sp := &SpawnpointRecord{
						Id:         id,
						Lat:        data.Lat,
						Lon:        data.Lon,
						DespawnSec: data.DespawnSec,
						Updated:    data.Updated,
						LastSeen:   data.LastSeen,
					}
					result[id] = sp
				} else {
					missingFromL2 = append(missingFromL2, id)
				}
			}
		}
	} else {
		missingFromL2 = ids
	}

	if len(missingFromL2) == 0 {
		return result, nil
	}

	// Second pass: Batch load from DB
	if err := s.loadFromDB(ctx, missingFromL2, result); err != nil {
		return result, err
	}

	return result, nil
}

func (s *SpawnpointLoader) loadFromDB(ctx context.Context, ids []int64, result map[int64]*SpawnpointRecord) error {
	if len(ids) == 0 {
		return nil
	}

	// Build IN clause
	query, args, err := sqlx.In("SELECT id, lat, lon, despawn_sec, updated, last_seen FROM spawnpoint WHERE id IN (?)", ids)
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	query = s.db.Rebind(query)

	var records []SpawnpointRecord
	if err := s.db.SelectContext(ctx, &records, query, args...); err != nil {
		return fmt.Errorf("failed to load from DB: %w", err)
	}

	// Prepare for batch Redis write
	toRedis := make(map[int64]SpawnpointData)

	for _, sp := range records {
		// Create a copy for result
		spCopy := sp
		result[sp.Id] = &spCopy

		// L1 cache is handled by decoder package

		// Prepare for L2 batch write
		if s.l2Cache != nil {
			toRedis[sp.Id] = SpawnpointData{
				Lat:        sp.Lat,
				Lon:        sp.Lon,
				DespawnSec: sp.DespawnSec,
				Updated:    sp.Updated,
				LastSeen:   sp.LastSeen,
			}
		}
	}

	// Batch write to Redis
	if len(toRedis) > 0 && s.l2Cache != nil {
		if err := s.l2Cache.BatchSetSpawnpoints(ctx, toRedis); err != nil {
			log.Warnf("Failed to batch write spawnpoints to Redis: %s", err)
		}
	}

	return nil
}

// SpawnpointL1Setter is a function type for setting L1 cache entries (avoids import cycle with decoder package)
type SpawnpointL1Setter func(id int64, lat, lon float64, despawnSec *int64, updated, lastSeen int64)

// LoadHotSpawnpointsOnStartup loads active spawnpoints into L1 cache and Redis
// l1Setter is a callback to populate L1 cache (provided by decoder package to avoid import cycles)
func (s *SpawnpointLoader) LoadHotSpawnpointsOnStartup(ctx context.Context, l1Setter SpawnpointL1Setter) error {
	if s.l2Cache == nil {
		return fmt.Errorf("L2 cache not available")
	}

	log.Info("Loading hot spawnpoints (last 7 days) into L1 cache and Redis...")
	startTime := time.Now()

	// Load spawnpoints seen in the last 7 days
	cutoff := time.Now().Unix() - (7 * 24 * 60 * 60)
	query := "SELECT id, lat, lon, despawn_sec, updated, last_seen FROM spawnpoint WHERE last_seen > ?"

	var records []SpawnpointRecord
	if err := s.db.SelectContext(ctx, &records, query, cutoff); err != nil {
		return fmt.Errorf("failed to load hot spawnpoints: %w", err)
	}

	log.Infof("Found %d hot spawnpoints to load", len(records))

	// Batch write to Redis and L1 cache in chunks
	chunkSize := 10000
	for i := 0; i < len(records); i += chunkSize {
		end := i + chunkSize
		if end > len(records) {
			end = len(records)
		}

		chunk := records[i:end]
		toRedis := make(map[int64]SpawnpointData)

		for _, sp := range chunk {
			toRedis[sp.Id] = SpawnpointData{
				Lat:        sp.Lat,
				Lon:        sp.Lon,
				DespawnSec: sp.DespawnSec,
				Updated:    sp.Updated,
				LastSeen:   sp.LastSeen,
			}

			// ALSO populate L1 cache via callback!
			if l1Setter != nil {
				l1Setter(sp.Id, sp.Lat, sp.Lon, sp.DespawnSec, sp.Updated, sp.LastSeen)
			}
		}

		if err := s.l2Cache.BatchSetSpawnpoints(ctx, toRedis); err != nil {
			log.Errorf("Failed to write chunk %d-%d to Redis: %s", i, end, err)
			continue
		}

		log.Debugf("Loaded spawnpoints %d-%d to L1 cache and Redis", i, end)
	}

	duration := time.Since(startTime)
	log.Infof("Loaded %d hot spawnpoints to L1 cache and Redis in %s", len(records), duration)

	return nil
}

// LoadFortsToRedis loads pokestops and gyms into Redis
func LoadFortsToRedis(ctx context.Context, db *sqlx.DB, l2Cache *L2Cache) error {
	if l2Cache == nil {
		return fmt.Errorf("L2 cache not available")
	}

	log.Info("Loading pokestops and gyms into Redis...")
	startTime := time.Now()

	// Check if Redis already has data loaded (to avoid unnecessary reload)
	// Sample a few keys from DB and check if they exist in Redis
	var sampleIds []string
	testQuery := "SELECT id FROM pokestop LIMIT 10"
	if err := db.SelectContext(ctx, &sampleIds, testQuery); err == nil && len(sampleIds) > 0 {
		existCount := 0
		for _, id := range sampleIds {
			key := fmt.Sprintf("pokestop:%s", id)
			if l2Cache.Exists(ctx, key) {
				existCount++
			}
		}

		// If 80%+ of sample keys exist, assume Redis is already loaded
		if existCount >= 8 {
			log.Info("Redis already contains fort data, skipping reload (use 'load_hot_on_startup: false' to disable this check)")
			return nil
		}
	}

	// Load pokestops
	pokestopQuery := "SELECT * FROM pokestop"
	rows, err := db.QueryxContext(ctx, pokestopQuery)
	if err != nil {
		return fmt.Errorf("failed to query pokestops: %w", err)
	}
	defer rows.Close()

	count := 0
	batch := make(map[string]interface{})
	for rows.Next() {
		pokestop := make(map[string]interface{})
		if err := rows.MapScan(pokestop); err != nil {
			log.Warnf("Failed to scan pokestop: %s", err)
			continue
		}

		// Handle both string and []byte types from MySQL
		var id string
		switch v := pokestop["id"].(type) {
		case string:
			id = v
		case []byte:
			id = string(v)
		default:
			log.Debugf("Unexpected type for pokestop ID: %T", pokestop["id"])
			continue
		}

		if id != "" {
			batch[fmt.Sprintf("pokestop:%s", id)] = pokestop
			count++

			if len(batch) >= 1000 {
				if err := l2Cache.BatchSet(ctx, batch); err != nil {
					log.Warnf("Failed to write pokestop batch: %s", err)
				}
				batch = make(map[string]interface{})
			}
		}
	}

	if len(batch) > 0 {
		if err := l2Cache.BatchSet(ctx, batch); err != nil {
			log.Warnf("Failed to write final pokestop batch: %s", err)
		}
	}

	log.Infof("Loaded %d pokestops to Redis", count)

	// Load gyms
	gymQuery := "SELECT * FROM gym"
	rows, err = db.QueryxContext(ctx, gymQuery)
	if err != nil {
		return fmt.Errorf("failed to query gyms: %w", err)
	}
	defer rows.Close()

	count = 0
	batch = make(map[string]interface{})
	for rows.Next() {
		gym := make(map[string]interface{})
		if err := rows.MapScan(gym); err != nil {
			log.Warnf("Failed to scan gym: %s", err)
			continue
		}

		// Handle both string and []byte types from MySQL
		var id string
		switch v := gym["id"].(type) {
		case string:
			id = v
		case []byte:
			id = string(v)
		default:
			log.Debugf("Unexpected type for gym ID: %T", gym["id"])
			continue
		}

		if id != "" {
			batch[fmt.Sprintf("gym:%s", id)] = gym
			count++

			if len(batch) >= 1000 {
				if err := l2Cache.BatchSet(ctx, batch); err != nil {
					log.Warnf("Failed to write gym batch: %s", err)
				}
				batch = make(map[string]interface{})
			}
		}
	}

	if len(batch) > 0 {
		if err := l2Cache.BatchSet(ctx, batch); err != nil {
			log.Warnf("Failed to write final gym batch: %s", err)
		}
	}

	duration := time.Since(startTime)
	log.Infof("Loaded %d gyms to Redis in total time %s", count, duration)

	return nil
}
