package cache

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"

	"golbat/config"
)

// FortSetter is a callback to populate L1 cache (avoids import cycles)
type FortSetter interface {
	SetPokestop(id string, data []byte) error
	SetGym(id string, data []byte) error
	SetStation(id string, data []byte) error
	SetRoute(id string, data []byte) error
}

// LoadFortsOnStartup loads all static objects from Redis (fast) or DB (fallback)
// Optimized for parallel loading and minimal memory allocation
func LoadFortsOnStartup(
	ctx context.Context,
	redisClient *redis.Client,
	dbConn *sqlx.DB,
	setter FortSetter,
) error {
	cfg := config.Config.Redis

	// Check if fort cache is enabled
	if !cfg.FortCacheEnabled {
		log.Info("Fort cache disabled, starting with empty L1 cache")
		return nil
	}

	start := time.Now()
	log.Info("Loading static objects from Redis cache...")

	// Try loading from Redis first (much faster: 10-20s)
	pokestopCount, gymCount, stationCount, routeCount, err := loadFortsFromRedis(ctx, redisClient, setter)

	if err != nil || (pokestopCount == 0 && gymCount == 0 && stationCount == 0 && routeCount == 0) {
		// Fallback to database if Redis is empty or failed (20-30s)
		log.Warnf("Redis cache unavailable (%v), using optimized DB fallback...", err)
		return loadFortsFromDatabase(ctx, dbConn, setter)
	}

	log.Infof("Loaded from Redis in %v: %d pokestops, %d gyms, %d stations, %d routes",
		time.Since(start), pokestopCount, gymCount, stationCount, routeCount)

	return nil
}

// loadFortsFromRedis loads forts from Redis hashes in parallel
func loadFortsFromRedis(
	ctx context.Context,
	redisClient *redis.Client,
	setter FortSetter,
) (int64, int64, int64, int64, error) {
	var pokestopCount, gymCount, stationCount, routeCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(4)

	// Load pokestops in parallel
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "fort_cache:pokestop", setter.SetPokestop)
		pokestopCount.Store(count)
	}()

	// Load gyms in parallel
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "fort_cache:gym", setter.SetGym)
		gymCount.Store(count)
	}()

	// Load stations in parallel
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "fort_cache:station", setter.SetStation)
		stationCount.Store(count)
	}()

	// Load routes in parallel
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "fort_cache:route", setter.SetRoute)
		routeCount.Store(count)
	}()

	wg.Wait()

	return pokestopCount.Load(), gymCount.Load(), stationCount.Load(), routeCount.Load(), nil
}

// loadFortHashFromRedis scans a Redis hash and populates L1 cache
// Uses HSCAN with cursor for memory-efficient streaming
func loadFortHashFromRedis(
	ctx context.Context,
	redisClient *redis.Client,
	hashKey string,
	setFunc func(string, []byte) error,
) int64 {
	var count int64
	cursor := uint64(0)
	scanSize := int64(1000) // Scan 1000 keys at a time

	for {
		// HSCAN returns key-value pairs
		keys, nextCursor, err := redisClient.HScan(ctx, hashKey, cursor, "*", scanSize).Result()
		if err != nil {
			log.Errorf("Failed to scan %s: %v", hashKey, err)
			break
		}

		// Process key-value pairs (keys are returned as [key1, val1, key2, val2, ...])
		for i := 0; i < len(keys); i += 2 {
			if i+1 >= len(keys) {
				break
			}

			id := keys[i]
			jsonData := []byte(keys[i+1])

			// Populate L1 cache via callback
			if err := setFunc(id, jsonData); err != nil {
				log.Debugf("Failed to set fort %s: %v", id, err)
				continue
			}

			count++
		}

		cursor = nextCursor
		if cursor == 0 {
			break // Scan complete
		}
	}

	return count
}

// loadFortsFromDatabase loads forts from database using OPTIMIZED streaming queries
// All 4 types loaded in parallel for maximum speed (20-30s)
func loadFortsFromDatabase(
	ctx context.Context,
	dbConn *sqlx.DB,
	setter FortSetter,
) error {
	start := time.Now()
	log.Info("Loading static objects from database (4x parallel)...")

	var pokestopCount, gymCount, stationCount, routeCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(4) // Load all 4 types in parallel

	// Load pokestops
	go func() {
		defer wg.Done()
		count := streamTableToCache(ctx, dbConn, "pokestop", setter.SetPokestop)
		pokestopCount.Store(count)
	}()

	// Load gyms
	go func() {
		defer wg.Done()
		count := streamTableToCache(ctx, dbConn, "gym", setter.SetGym)
		gymCount.Store(count)
	}()

	// Load stations
	go func() {
		defer wg.Done()
		count := streamTableToCache(ctx, dbConn, "station", setter.SetStation)
		stationCount.Store(count)
	}()

	// Load routes
	go func() {
		defer wg.Done()
		count := streamTableToCache(ctx, dbConn, "route", setter.SetRoute)
		routeCount.Store(count)
	}()

	wg.Wait()

	log.Infof("Loaded from DB in %v: %d pokestops, %d gyms, %d stations, %d routes",
		time.Since(start), pokestopCount.Load(), gymCount.Load(), 
		stationCount.Load(), routeCount.Load())

	return nil
}

// streamTableToCache uses streaming SELECT to load table data
// Memory-efficient: doesn't load entire table into memory
func streamTableToCache(
	ctx context.Context,
	dbConn *sqlx.DB,
	tableName string,
	setFunc func(string, []byte) error,
) int64 {
	query := "SELECT * FROM " + tableName
	rows, err := dbConn.QueryxContext(ctx, query)
	if err != nil {
		log.Errorf("Failed to query %s: %v", tableName, err)
		return 0
	}
	defer rows.Close()

	var count int64
	lastLog := time.Now()

	for rows.Next() {
		// Scan into map for flexibility (handles all columns)
		result := make(map[string]interface{})
		if err := rows.MapScan(result); err != nil {
			log.Debugf("Failed to scan row: %v", err)
			continue
		}

		// Extract ID
		id, ok := result["id"].([]byte)
		if !ok {
			// Try string type
			if idStr, ok := result["id"].(string); ok {
				id = []byte(idStr)
			} else {
				continue
			}
		}

		// Convert to JSON for storage
		jsonData, err := json.Marshal(result)
		if err != nil {
			continue
		}

		// Populate L1 cache
		if err := setFunc(string(id), jsonData); err != nil {
			log.Debugf("Failed to set %s: %v", string(id), err)
			continue
		}

		count++

		// Progress logging every 10 seconds
		if time.Since(lastLog) > 10*time.Second {
			log.Infof("Loading %s: %d loaded...", tableName, count)
			lastLog = time.Now()
		}
	}

	return count
}

// UpdateFortCacheAsync updates Redis fort cache asynchronously
// Non-blocking: uses goroutine + pipeline for minimal overhead (~1ms)
func UpdateFortCacheAsync(redisClient *redis.Client, fortType string, id string, data []byte) {
	cfg := config.Config.Redis

	if !cfg.FortCacheEnabled || redisClient == nil {
		return // Fort cache disabled
	}

	// Run in goroutine for non-blocking behavior (hundreds of thousands per second)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		hashKey := "fort_cache:" + fortType
		ttl := time.Duration(cfg.FortCacheTTLHours) * time.Hour

		// Use pipeline for efficiency (single round trip)
		pipe := redisClient.Pipeline()
		pipe.HSet(ctx, hashKey, id, data)
		pipe.Expire(ctx, hashKey, ttl)

		if _, err := pipe.Exec(ctx); err != nil {
			// Don't spam logs, this is non-critical
			log.Debugf("Failed to update %s cache for %s: %v", fortType, id, err)
		}
	}()
}

// ClearFortCache removes all forts from Redis cache
// Useful for maintenance or full reload
func ClearFortCache(ctx context.Context, redisClient *redis.Client) error {
	if redisClient == nil {
		return nil
	}

	log.Info("Clearing Redis fort cache...")

	pipe := redisClient.Pipeline()
	pipe.Del(ctx, "fort_cache:pokestop")
	pipe.Del(ctx, "fort_cache:gym")
	pipe.Del(ctx, "fort_cache:station")
	pipe.Del(ctx, "fort_cache:route")

	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Errorf("Failed to clear fort cache: %v", err)
		return err
	}

	log.Info("Fort cache cleared")
	return nil
}

// GetFortCacheStats returns cache statistics
func GetFortCacheStats(ctx context.Context, redisClient *redis.Client) (pokestopCount, gymCount, stationCount, routeCount int64, err error) {
	if redisClient == nil {
		return 0, 0, 0, 0, nil
	}

	pipe := redisClient.Pipeline()
	pokestopCmd := pipe.HLen(ctx, "fort_cache:pokestop")
	gymCmd := pipe.HLen(ctx, "fort_cache:gym")
	stationCmd := pipe.HLen(ctx, "fort_cache:station")
	routeCmd := pipe.HLen(ctx, "fort_cache:route")

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, 0, 0, 0, err
	}

	return pokestopCmd.Val(), gymCmd.Val(), stationCmd.Val(), routeCmd.Val(), nil
}

func init() {
	// Set GOMAXPROCS for optimal parallel performance
	if runtime.GOMAXPROCS(0) < 2 {
		runtime.GOMAXPROCS(2)
	}
}
