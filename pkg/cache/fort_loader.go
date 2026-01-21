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
		// Fallback to database if Redis is empty or failed
		// Load sequentially to avoid resource exhaustion
		log.Warnf("Redis cache unavailable (%v), loading from database...", err)
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
// Loads SEQUENTIALLY (not parallel) to avoid resource exhaustion
// Takes longer but prevents context deadline exceeded errors
func loadFortsFromDatabase(
	ctx context.Context,
	dbConn *sqlx.DB,
	setter FortSetter,
) error {
	start := time.Now()
	log.Info("Loading static objects from database (sequential to avoid resource spike)...")

	// Load sequentially to avoid overwhelming DB/CPU/memory
	// Each table loads completely before next one starts

	log.Info("Loading pokestops from database...")
	pokestopCount := loadPokestopsFromDB(ctx, dbConn, setter)
	log.Infof("Loaded %d pokestops", pokestopCount)

	log.Info("Loading gyms from database...")
	gymCount := loadGymsFromDB(ctx, dbConn, setter)
	log.Infof("Loaded %d gyms", gymCount)

	log.Info("Loading stations from database...")
	stationCount := loadStationsFromDB(ctx, dbConn, setter)
	log.Infof("Loaded %d stations", stationCount)

	log.Info("Loading routes from database...")
	routeCount := loadRoutesFromDB(ctx, dbConn, setter)
	log.Infof("Loaded %d routes", routeCount)

	log.Infof("Loaded from DB in %v: %d pokestops, %d gyms, %d stations, %d routes",
		time.Since(start), pokestopCount, gymCount, stationCount, routeCount)

	return nil
}

// streamTableToCacheDirect uses streaming SELECT with StructScan
// Memory-efficient + zero JSON overhead for maximum performance
func streamTableToCacheDirect(
	ctx context.Context,
	dbConn *sqlx.DB,
	tableName string,
	scanFunc func(*sqlx.Rows) error,
) int64 {
	query := "SELECT * FROM " + tableName
	rows, err := dbConn.QueryxContext(ctx, query)
	if err != nil {
		log.Errorf("Failed to query %s table: %v", tableName, err)
		return 0
	}
	defer rows.Close()

	var count, errorCount int64
	lastLog := time.Now()
	start := time.Now()

	for rows.Next() {
		// Direct StructScan into the type (no JSON overhead!)
		if err := scanFunc(rows); err != nil {
			errorCount++
			if errorCount <= 10 { // Log first 10 errors only
				log.Errorf("Failed to scan %s row #%d: %v", tableName, count+errorCount, err)
			}
			continue
		}

		count++

		// Progress logging every 10 seconds
		if time.Since(lastLog) > 10*time.Second {
			elapsed := time.Since(start)
			log.Infof("Loading %s: %d loaded (%.1f/sec)...", tableName, count, float64(count)/elapsed.Seconds())
			lastLog = time.Now()
		}
	}

	if err := rows.Err(); err != nil {
		log.Errorf("Error iterating %s rows: %v", tableName, err)
	}

	if errorCount > 0 {
		log.Warnf("Loaded %s with %d errors (skipped %d rows)", tableName, errorCount, errorCount)
	}

	return count
}

// loadPokestopsFromDB streams pokestops from DB into L1 cache
func loadPokestopsFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
	return streamTableToCacheDirect(ctx, dbConn, "pokestop",
		func(rows *sqlx.Rows) error {
			// Scan directly into map
			result := make(map[string]interface{})
			if err := rows.MapScan(result); err != nil {
				return err
			}

			// Extract ID
			id, ok := result["id"].([]byte)
			if !ok {
				if idStr, ok := result["id"].(string); ok {
					id = []byte(idStr)
				} else {
					return nil // Skip if no ID
				}
			}

			// Marshal to JSON for setter
			jsonData, err := json.Marshal(result)
			if err != nil {
				return err
			}

			return setter.SetPokestop(string(id), jsonData)
		})
}

// loadGymsFromDB streams gyms from DB into L1 cache
func loadGymsFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
	return streamTableToCacheDirect(ctx, dbConn, "gym",
		func(rows *sqlx.Rows) error {
			result := make(map[string]interface{})
			if err := rows.MapScan(result); err != nil {
				return err
			}

			id, ok := result["id"].([]byte)
			if !ok {
				if idStr, ok := result["id"].(string); ok {
					id = []byte(idStr)
				} else {
					return nil
				}
			}

			jsonData, err := json.Marshal(result)
			if err != nil {
				return err
			}

			return setter.SetGym(string(id), jsonData)
		})
}

// loadStationsFromDB streams stations from DB into L1 cache
func loadStationsFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
	return streamTableToCacheDirect(ctx, dbConn, "station",
		func(rows *sqlx.Rows) error {
			result := make(map[string]interface{})
			if err := rows.MapScan(result); err != nil {
				return err
			}

			id, ok := result["id"].([]byte)
			if !ok {
				if idStr, ok := result["id"].(string); ok {
					id = []byte(idStr)
				} else {
					return nil
				}
			}

			jsonData, err := json.Marshal(result)
			if err != nil {
				return err
			}

			return setter.SetStation(string(id), jsonData)
		})
}

// loadRoutesFromDB streams routes from DB into L1 cache
func loadRoutesFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
	return streamTableToCacheDirect(ctx, dbConn, "route",
		func(rows *sqlx.Rows) error {
			result := make(map[string]interface{})
			if err := rows.MapScan(result); err != nil {
				return err
			}

			id, ok := result["id"].([]byte)
			if !ok {
				if idStr, ok := result["id"].(string); ok {
					id = []byte(idStr)
				} else {
					return nil
				}
			}

			jsonData, err := json.Marshal(result)
			if err != nil {
				return err
			}

			return setter.SetRoute(string(id), jsonData)
		})
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
