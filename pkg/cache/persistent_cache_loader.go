package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v4"

	"golbat/config"
)

// PersistentCacheSetter is a callback to populate L1 cache (avoids import cycles)
type PersistentCacheSetter interface {
	SetPokestop(id string, data []byte) error
	SetGym(id string, data []byte) error
	SetStation(id string, data []byte) error
	SetRoute(id string, data []byte) error
	SetSpawnpoint(id string, data []byte) error
}

// LoadPersistentCacheOnStartup loads all static objects from Redis (fast) or DB (fallback)
// Optimized for parallel loading and minimal memory allocation
func LoadPersistentCacheOnStartup(
	ctx context.Context,
	redisClient *redis.Client,
	dbConn *sqlx.DB,
	setter PersistentCacheSetter,
) error {
	cfg := config.Config.Redis

	// Check if fort cache is enabled
	if !cfg.PersistentCacheEnabled {
		log.Info("Fort cache disabled, starting with empty L1 cache")
		return nil
	}

	start := time.Now()
	log.Info("Loading all persistent cache data from Redis...")

	// Try loading from Redis first (much faster: 10-60s depending on data size)
	pokestopCount, gymCount, stationCount, routeCount, spawnpointCount, err := loadPersistentCacheFromRedis(ctx, redisClient, setter)

	if err != nil || (pokestopCount == 0 && gymCount == 0 && stationCount == 0 && routeCount == 0 && spawnpointCount == 0) {
		// Fallback to database if Redis is empty or failed
		// Load sequentially with smart batching to avoid resource exhaustion
		log.Warnf("Redis cache unavailable (%v), loading from database (this will take several minutes)...", err)
		return loadPersistentCacheFromDatabase(ctx, dbConn, setter)
	}

	log.Infof("Loaded from Redis in %v: %d pokestops, %d gyms, %d stations, %d routes, %d spawnpoints",
		time.Since(start), pokestopCount, gymCount, stationCount, routeCount, spawnpointCount)

	return nil
}

// loadPersistentCacheFromRedis loads all persistent cache data from Redis hashes in parallel
func loadPersistentCacheFromRedis(
	ctx context.Context,
	redisClient *redis.Client,
	setter PersistentCacheSetter,
) (int64, int64, int64, int64, int64, error) {
	var pokestopCount, gymCount, stationCount, routeCount, spawnpointCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(5) // Load all 5 types in parallel

	// Load pokestops
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "persistent_cache:pokestop", setter.SetPokestop)
		pokestopCount.Store(count)
	}()

	// Load gyms
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "persistent_cache:gym", setter.SetGym)
		gymCount.Store(count)
	}()

	// Load stations
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "persistent_cache:station", setter.SetStation)
		stationCount.Store(count)
	}()

	// Load routes
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "persistent_cache:route", setter.SetRoute)
		routeCount.Store(count)
	}()

	// Load spawnpoints (largest dataset - 4.5M records)
	go func() {
		defer wg.Done()
		count := loadFortHashFromRedis(ctx, redisClient, "persistent_cache:spawnpoint", setter.SetSpawnpoint)
		spawnpointCount.Store(count)
	}()

	wg.Wait()

	return pokestopCount.Load(), gymCount.Load(), stationCount.Load(), routeCount.Load(), spawnpointCount.Load(), nil
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

// loadPersistentCacheFromDatabase loads forts from database using OPTIMIZED streaming queries
// Loads SEQUENTIALLY (not parallel) to avoid resource exhaustion
// Takes longer but prevents context deadline exceeded errors
func loadPersistentCacheFromDatabase(
	ctx context.Context,
	dbConn *sqlx.DB,
	setter PersistentCacheSetter,
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

	log.Info("Loading hot spawnpoints from database (last 7 days)...")
	spawnpointCount := loadSpawnpointsFromDB(ctx, dbConn, setter)
	log.Infof("Loaded %d spawnpoints", spawnpointCount)

	log.Infof("Loaded from DB in %v: %d pokestops, %d gyms, %d stations, %d routes, %d spawnpoints",
		time.Since(start), pokestopCount, gymCount, stationCount, routeCount, spawnpointCount)

	return nil
}

// streamTableToCacheDirect uses streaming SELECT with StructScan
// Memory-efficient + zero JSON overhead for maximum performance
func streamTableToCacheDirect(
	ctx context.Context,
	dbConn *sqlx.DB,
	tableName string,
	whereClause string,
	scanFunc func(*sqlx.Rows) error,
) int64 {
	// Count total rows first for progress %
	countQuery := "SELECT COUNT(*) FROM " + tableName
	if whereClause != "" {
		countQuery += " WHERE " + whereClause
	}
	var totalRows int64
	if err := dbConn.GetContext(ctx, &totalRows, countQuery); err != nil {
		log.Warnf("Failed to count %s rows: %v (progress % will not be shown)", tableName, err)
		totalRows = 0 // Continue without total
	} else {
		log.Infof("Processing %d rows of %s from database...", totalRows, tableName)
	}

	query := "SELECT * FROM " + tableName
	if whereClause != "" {
		query += " WHERE " + whereClause
	}
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
			if totalRows > 0 {
				pct := float64(count) / float64(totalRows) * 100
				log.Infof("Loading %s: %d/%d (%.1f%%, %.1f/sec)...", tableName, count, totalRows, pct, float64(count)/elapsed.Seconds())
			} else {
				log.Infof("Loading %s: %d loaded (%.1f/sec)...", tableName, count, float64(count)/elapsed.Seconds())
			}
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
func loadPokestopsFromDB(ctx context.Context, dbConn *sqlx.DB, setter PersistentCacheSetter) int64 {
	// Import decoder locally to avoid import cycle
	type Pokestop struct {
		Id                           string      `db:"id"`
		Lat                          float64     `db:"lat"`
		Lon                          float64     `db:"lon"`
		Name                         null.String `db:"name"`
		Url                          null.String `db:"url"`
		Enabled                      null.Bool   `db:"enabled"` // TINYINT unsigned → StructScan converts to bool
		LureExpireTimestamp          null.Int    `db:"lure_expire_timestamp"`
		LastModifiedTimestamp        null.Int    `db:"last_modified_timestamp"`
		Updated                      int64       `db:"updated"`
		QuestType                    null.Int    `db:"quest_type"`
		QuestTimestamp               null.Int    `db:"quest_timestamp"`
		QuestTarget                  null.Int    `db:"quest_target"`
		QuestConditions              null.String `db:"quest_conditions"`
		QuestRewards                 null.String `db:"quest_rewards"`
		QuestTemplate                null.String `db:"quest_template"`
		QuestTitle                   null.String `db:"quest_title"`
		QuestRewardType              null.Int    `db:"quest_reward_type"`   // VIRTUAL/GENERATED
		QuestItemId                  null.Int    `db:"quest_item_id"`       // VIRTUAL/GENERATED
		QuestRewardAmount            null.Int    `db:"quest_reward_amount"` // VIRTUAL/GENERATED
		QuestPokemonId               null.Int    `db:"quest_pokemon_id"`    // VIRTUAL/GENERATED
		QuestExpiry                  null.Int    `db:"quest_expiry"`
		AlternativeQuestType         null.Int    `db:"alternative_quest_type"`
		AlternativeQuestTimestamp    null.Int    `db:"alternative_quest_timestamp"`
		AlternativeQuestTarget       null.Int    `db:"alternative_quest_target"`
		AlternativeQuestConditions   null.String `db:"alternative_quest_conditions"`
		AlternativeQuestRewards      null.String `db:"alternative_quest_rewards"`
		AlternativeQuestTemplate     null.String `db:"alternative_quest_template"`
		AlternativeQuestTitle        null.String `db:"alternative_quest_title"`
		AlternativeQuestPokemonId    null.Int    `db:"alternative_quest_pokemon_id"`    // VIRTUAL/GENERATED
		AlternativeQuestRewardType   null.Int    `db:"alternative_quest_reward_type"`   // VIRTUAL/GENERATED
		AlternativeQuestItemId       null.Int    `db:"alternative_quest_item_id"`       // VIRTUAL/GENERATED
		AlternativeQuestRewardAmount null.Int    `db:"alternative_quest_reward_amount"` // VIRTUAL/GENERATED
		AlternativeQuestExpiry       null.Int    `db:"alternative_quest_expiry"`
		CellId                       null.Int    `db:"cell_id"`
		Deleted                      bool        `db:"deleted"` // TINYINT unsigned → StructScan converts to bool
		LureId                       null.Int    `db:"lure_id"`
		FirstSeenTimestamp           int64       `db:"first_seen_timestamp"`
		SponsorId                    null.Int    `db:"sponsor_id"`
		PartnerId                    null.String `db:"partner_id"`
		ArScanEligible               null.Int    `db:"ar_scan_eligible"`
		PowerUpLevel                 null.Int    `db:"power_up_level"`
		PowerUpPoints                null.Int    `db:"power_up_points"`
		PowerUpEndTimestamp          null.Int    `db:"power_up_end_timestamp"`
		Description                  null.String `db:"description"`
		ShowcasePokemonId            null.Int    `db:"showcase_pokemon_id"`
		ShowcasePokemonFormId        null.Int    `db:"showcase_pokemon_form_id"`
		ShowcasePokemonTypeId        null.Int    `db:"showcase_pokemon_type_id"`
		ShowcaseRankingStandard      null.Int    `db:"showcase_ranking_standard"`
		ShowcaseFocus                null.String `db:"showcase_focus"` // TEXT (JSON), not INT
		ShowcaseExpiry               null.Int    `db:"showcase_expiry"`
		ShowcaseRankings             null.String `db:"showcase_rankings"`
	}

	// Only load recent pokestops (configurable max age)
	cfg := config.Config.Redis.PersistentCacheConfig
	maxAgeDays := cfg.GetMaxAgeDays("pokestop")
	cutoffTime := time.Now().Unix() - int64(maxAgeDays*86400) // Convert days to seconds
	whereClause := fmt.Sprintf("updated > %d", cutoffTime)

	return streamTableToCacheDirect(ctx, dbConn, "pokestop", whereClause,
		func(rows *sqlx.Rows) error {
			var stop Pokestop
			if err := rows.StructScan(&stop); err != nil {
				return err
			}

			// Marshal to JSON for setter
			jsonData, err := json.Marshal(stop)
			if err != nil {
				return err
			}

			return setter.SetPokestop(stop.Id, jsonData)
		})
}

// loadGymsFromDB streams gyms from DB into L1 cache
func loadGymsFromDB(ctx context.Context, dbConn *sqlx.DB, setter PersistentCacheSetter) int64 {
	// Lightweight struct matching DB schema (avoids import cycle with decoder)
	type Gym struct {
		Id                     string      `db:"id"`
		Lat                    float64     `db:"lat"`
		Lon                    float64     `db:"lon"`
		Name                   null.String `db:"name"`
		Url                    null.String `db:"url"`
		LastModifiedTimestamp  null.Int    `db:"last_modified_timestamp"`
		RaidEndTimestamp       null.Int    `db:"raid_end_timestamp"`
		RaidSpawnTimestamp     null.Int    `db:"raid_spawn_timestamp"`
		RaidBattleTimestamp    null.Int    `db:"raid_battle_timestamp"`
		Updated                int64       `db:"updated"`
		RaidPokemonId          null.Int    `db:"raid_pokemon_id"`
		GuardingPokemonId      null.Int    `db:"guarding_pokemon_id"`
		GuardingPokemonDisplay null.String `db:"guarding_pokemon_display"` // TEXT (added in migration 28)
		AvailableSlots         null.Int    `db:"available_slots"`
		AvailbleSlots          null.Int    `db:"availble_slots"` // VIRTUAL (typo in schema but exists)
		TeamId                 null.Int    `db:"team_id"`
		RaidLevel              null.Int    `db:"raid_level"`
		Enabled                null.Bool   `db:"enabled"`          // TINYINT unsigned → StructScan converts to bool
		ExRaidEligible         null.Bool   `db:"ex_raid_eligible"` // TINYINT unsigned → StructScan converts to bool
		InBattle               null.Bool   `db:"in_battle"`        // TINYINT unsigned → StructScan converts to bool
		RaidPokemonMove1       null.Int    `db:"raid_pokemon_move_1"`
		RaidPokemonMove2       null.Int    `db:"raid_pokemon_move_2"`
		RaidPokemonForm        null.Int    `db:"raid_pokemon_form"`
		RaidPokemonAlignment   null.Int    `db:"raid_pokemon_alignment"` // Added in migration 18
		RaidPokemonCp          null.Int    `db:"raid_pokemon_cp"`
		RaidIsExclusive        null.Bool   `db:"raid_is_exclusive"` // TINYINT unsigned → StructScan converts to bool
		CellId                 null.Int    `db:"cell_id"`
		Deleted                bool        `db:"deleted"` // TINYINT unsigned → StructScan converts to bool
		TotalCp                null.Int    `db:"total_cp"`
		FirstSeenTimestamp     int64       `db:"first_seen_timestamp"`
		RaidPokemonGender      null.Int    `db:"raid_pokemon_gender"`
		SponsorId              null.Int    `db:"sponsor_id"`
		PartnerId              null.String `db:"partner_id"`
		RaidPokemonCostume     null.Int    `db:"raid_pokemon_costume"`
		RaidPokemonEvolution   null.Int    `db:"raid_pokemon_evolution"`
		ArScanEligible         null.Int    `db:"ar_scan_eligible"`
		PowerUpLevel           null.Int    `db:"power_up_level"`
		PowerUpPoints          null.Int    `db:"power_up_points"`
		PowerUpEndTimestamp    null.Int    `db:"power_up_end_timestamp"`
		Description            null.String `db:"description"` // TEXT
		Defenders              null.String `db:"defenders"`   // TEXT (added in migration 40)
		Rsvps                  null.String `db:"rsvps"`       // TEXT (added in migration 46)
	}

	// Only load recent gyms (configurable max age)
	cfg := config.Config.Redis.PersistentCacheConfig
	maxAgeDays := cfg.GetMaxAgeDays("gym")
	cutoffTime := time.Now().Unix() - int64(maxAgeDays*86400)
	whereClause := fmt.Sprintf("updated > %d", cutoffTime)

	return streamTableToCacheDirect(ctx, dbConn, "gym", whereClause,
		func(rows *sqlx.Rows) error {
			var gym Gym
			if err := rows.StructScan(&gym); err != nil {
				return err
			}

			jsonData, err := json.Marshal(gym)
			if err != nil {
				return err
			}

			return setter.SetGym(gym.Id, jsonData)
		})
}

// loadStationsFromDB streams stations from DB into L1 cache
func loadStationsFromDB(ctx context.Context, dbConn *sqlx.DB, setter PersistentCacheSetter) int64 {
	type Station struct {
		Id                        string      `db:"id"`
		Lat                       float64     `db:"lat"`
		Lon                       float64     `db:"lon"`
		Name                      string      `db:"name"`
		CellId                    int64       `db:"cell_id"`
		StartTime                 int64       `db:"start_time"`
		EndTime                   int64       `db:"end_time"`
		CooldownComplete          int64       `db:"cooldown_complete"`
		IsBattleAvailable         bool        `db:"is_battle_available"`
		IsInactive                bool        `db:"is_inactive"`
		Updated                   int64       `db:"updated"`
		BattleLevel               null.Int    `db:"battle_level"`
		BattleStart               null.Int    `db:"battle_start"`
		BattleEnd                 null.Int    `db:"battle_end"`
		BattlePokemonId           null.Int    `db:"battle_pokemon_id"`
		BattlePokemonForm         null.Int    `db:"battle_pokemon_form"`
		BattlePokemonCostume      null.Int    `db:"battle_pokemon_costume"`
		BattlePokemonGender       null.Int    `db:"battle_pokemon_gender"`
		BattlePokemonAlignment    null.Int    `db:"battle_pokemon_alignment"`
		BattlePokemonBreadMode    null.Int    `db:"battle_pokemon_bread_mode"`
		BattlePokemonMove1        null.Int    `db:"battle_pokemon_move_1"`
		BattlePokemonMove2        null.Int    `db:"battle_pokemon_move_2"`
		BattlePokemonStamina      null.Int    `db:"battle_pokemon_stamina"`
		BattlePokemonCpMultiplier null.Float  `db:"battle_pokemon_cp_multiplier"`
		TotalStationedPokemon     null.Int    `db:"total_stationed_pokemon"`
		TotalStationedGmax        null.Int    `db:"total_stationed_gmax"`
		StationedPokemon          null.String `db:"stationed_pokemon"`
	}

	// Only load recent stations (configurable max age)
	cfg := config.Config.Redis.PersistentCacheConfig
	maxAgeDays := cfg.GetMaxAgeDays("station")
	cutoffTime := time.Now().Unix() - int64(maxAgeDays*86400)
	whereClause := fmt.Sprintf("updated > %d", cutoffTime)

	return streamTableToCacheDirect(ctx, dbConn, "station", whereClause,
		func(rows *sqlx.Rows) error {
			var station Station
			if err := rows.StructScan(&station); err != nil {
				return err
			}

			jsonData, err := json.Marshal(station)
			if err != nil {
				return err
			}

			return setter.SetStation(station.Id, jsonData)
		})
}

// loadRoutesFromDB streams routes from DB into L1 cache
func loadRoutesFromDB(ctx context.Context, dbConn *sqlx.DB, setter PersistentCacheSetter) int64 {
	type Route struct {
		Id               string      `db:"id"`
		Name             string      `db:"name"`
		Shortcode        null.String `db:"shortcode"`
		Description      null.String `db:"description"`
		DistanceMeters   null.Int    `db:"distance_meters"`
		DurationSeconds  null.Int    `db:"duration_seconds"`
		StartFortId      null.String `db:"start_fort_id"`
		StartLat         null.Float  `db:"start_lat"`
		StartLon         null.Float  `db:"start_lon"`
		StartImage       null.String `db:"start_image"`
		EndFortId        null.String `db:"end_fort_id"`
		EndLat           null.Float  `db:"end_lat"`
		EndLon           null.Float  `db:"end_lon"`
		EndImage         null.String `db:"end_image"`
		Updated          int64       `db:"updated"`
		Reversible       null.Bool   `db:"reversible"`
		Tags             null.String `db:"tags"`
		Type             null.Int    `db:"type"`
		Version          null.Int    `db:"version"`
		Waypoints        null.String `db:"waypoints"`
		Image            null.String `db:"image"`
		ImageBorderColor null.String `db:"image_border_color"`
	}

	// Only load recent routes (configurable max age)
	cfg := config.Config.Redis.PersistentCacheConfig
	maxAgeDays := cfg.GetMaxAgeDays("route")
	cutoffTime := time.Now().Unix() - int64(maxAgeDays*86400)
	whereClause := fmt.Sprintf("updated > %d", cutoffTime)

	return streamTableToCacheDirect(ctx, dbConn, "route", whereClause,
		func(rows *sqlx.Rows) error {
			var route Route
			if err := rows.StructScan(&route); err != nil {
				return err
			}

			jsonData, err := json.Marshal(route)
			if err != nil {
				return err
			}

			return setter.SetRoute(route.Id, jsonData)
		})
}

// loadSpawnpointsFromDB streams hot spawnpoints (last 7 days) from DB into L1 cache
// Uses custom query with WHERE clause to only load hot spawnpoints
func loadSpawnpointsFromDB(ctx context.Context, dbConn *sqlx.DB, setter PersistentCacheSetter) int64 {
	type Spawnpoint struct {
		Id         int64    `db:"id"`
		Lat        float64  `db:"lat"`
		Lon        float64  `db:"lon"`
		Updated    int64    `db:"updated"`
		LastSeen   int64    `db:"last_seen"`
		DespawnSec null.Int `db:"despawn_sec"`
	}

	// Load hot spawnpoints (configurable max age) - reduces dataset significantly
	cfg := config.Config.Redis.PersistentCacheConfig
	maxAgeDays := cfg.GetMaxAgeDays("spawnpoint")
	query := fmt.Sprintf(`
		SELECT id, lat, lon, updated, last_seen, despawn_sec 
		FROM spawnpoint 
		WHERE last_seen > UNIX_TIMESTAMP(DATE_SUB(NOW(), INTERVAL %d DAY))
		ORDER BY id
	`, maxAgeDays)

	rows, err := dbConn.QueryxContext(ctx, query)
	if err != nil {
		log.Errorf("Failed to query hot spawnpoints: %v", err)
		return 0
	}
	defer rows.Close()

	var count, errorCount int64
	lastLog := time.Now()
	start := time.Now()

	for rows.Next() {
		var sp Spawnpoint
		if err := rows.StructScan(&sp); err != nil {
			errorCount++
			if errorCount <= 10 {
				log.Errorf("Failed to scan spawnpoint row #%d: %v", count+errorCount, err)
			}
			continue
		}

		jsonData, err := json.Marshal(sp)
		if err != nil {
			continue
		}

		// Convert int64 ID to string for setter
		if err := setter.SetSpawnpoint(fmt.Sprintf("%d", sp.Id), jsonData); err != nil {
			log.Debugf("Failed to set spawnpoint %d: %v", sp.Id, err)
			continue
		}

		count++

		// Progress logging every 10 seconds
		if time.Since(lastLog) > 10*time.Second {
			elapsed := time.Since(start)
			log.Infof("Loading spawnpoint: %d loaded (%.1f/sec)...", count, float64(count)/elapsed.Seconds())
			lastLog = time.Now()
		}
	}

	if errorCount > 0 {
		log.Warnf("Loaded spawnpoints with %d errors (skipped %d rows)", errorCount, errorCount)
	}

	return count
}

// UpdatePersistentCacheAsync updates Redis fort cache asynchronously
// Non-blocking: uses goroutine + pipeline for minimal overhead (~1ms)
func UpdatePersistentCacheAsync(redisClient *redis.Client, fortType string, id string, data []byte) {
	cfg := config.Config.Redis

	if !cfg.PersistentCacheEnabled || redisClient == nil {
		return // Fort cache disabled
	}

	// Run in goroutine for non-blocking behavior (hundreds of thousands per second)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		hashKey := "persistent_cache:" + fortType
		ttl := time.Duration(cfg.PersistentCacheTTLHours) * time.Hour

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

// ClearPersistentCache removes all forts from Redis cache
// Useful for maintenance or full reload
func ClearPersistentCache(ctx context.Context, redisClient *redis.Client) error {
	if redisClient == nil {
		return nil
	}

	log.Info("Clearing Redis fort cache...")

	pipe := redisClient.Pipeline()
	pipe.Del(ctx, "persistent_cache:pokestop")
	pipe.Del(ctx, "persistent_cache:gym")
	pipe.Del(ctx, "persistent_cache:station")
	pipe.Del(ctx, "persistent_cache:route")
	pipe.Del(ctx, "persistent_cache:spawnpoint")

	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Errorf("Failed to clear fort cache: %v", err)
		return err
	}

	log.Info("Fort cache cleared")
	return nil
}

// GetPersistentCacheStats returns cache statistics
func GetPersistentCacheStats(ctx context.Context, redisClient *redis.Client) (pokestopCount, gymCount, stationCount, routeCount, spawnpointCount int64, err error) {
	if redisClient == nil {
		return 0, 0, 0, 0, 0, nil
	}

	pipe := redisClient.Pipeline()
	pokestopCmd := pipe.HLen(ctx, "persistent_cache:pokestop")
	gymCmd := pipe.HLen(ctx, "persistent_cache:gym")
	stationCmd := pipe.HLen(ctx, "persistent_cache:station")
	routeCmd := pipe.HLen(ctx, "persistent_cache:route")
	spawnpointCmd := pipe.HLen(ctx, "persistent_cache:spawnpoint")

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, 0, 0, 0, 0, err
	}

	return pokestopCmd.Val(), gymCmd.Val(), stationCmd.Val(), routeCmd.Val(), spawnpointCmd.Val(), nil
}

// StartPersistentCacheTrimmer starts a background goroutine to periodically trim stale data from Redis
func StartPersistentCacheTrimmer(redisClient *redis.Client) {
	cfg := config.Config.Redis.PersistentCacheConfig

	if !cfg.TrimEnabled {
		log.Info("Persistent cache trimming disabled")
		return
	}

	trimInterval := time.Duration(cfg.TrimIntervalHours) * time.Hour
	if trimInterval == 0 {
		trimInterval = 24 * time.Hour // Default: trim every 24 hours
	}

	log.Infof("Starting persistent cache trimmer (interval: %v)", trimInterval)

	go func() {
		ticker := time.NewTicker(trimInterval)
		defer ticker.Stop()

		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			trimPersistentCache(ctx, redisClient)
			cancel()
		}
	}()
}

// trimPersistentCache removes stale entries from all persistent_cache:* hashes
func trimPersistentCache(ctx context.Context, redisClient *redis.Client) {
	start := time.Now()
	log.Info("Starting persistent cache trimming...")

	cfg := config.Config.Redis.PersistentCacheConfig

	// Trim each object type based on its max age
	totalDeleted := 0

	totalDeleted += trimCacheHash(ctx, redisClient, "persistent_cache:pokestop", cfg.GetMaxAgeDays("pokestop"))
	totalDeleted += trimCacheHash(ctx, redisClient, "persistent_cache:gym", cfg.GetMaxAgeDays("gym"))
	totalDeleted += trimCacheHash(ctx, redisClient, "persistent_cache:station", cfg.GetMaxAgeDays("station"))
	totalDeleted += trimCacheHash(ctx, redisClient, "persistent_cache:route", cfg.GetMaxAgeDays("route"))
	totalDeleted += trimCacheHash(ctx, redisClient, "persistent_cache:spawnpoint", cfg.GetMaxAgeDays("spawnpoint"))

	log.Infof("Persistent cache trimming complete: deleted %d stale entries in %v", totalDeleted, time.Since(start))
}

// trimCacheHash removes entries older than maxAgeDays from a Redis hash
func trimCacheHash(ctx context.Context, redisClient *redis.Client, hashKey string, maxAgeDays int) int {
	cutoffTime := time.Now().Unix() - int64(maxAgeDays*86400)
	deleted := 0

	// Use HSCAN to iterate through all entries efficiently
	var cursor uint64
	for {
		var keys []string
		var values []string
		var err error

		keys, cursor, err = redisClient.HScan(ctx, hashKey, cursor, "*", 1000).Result()
		if err != nil {
			log.Errorf("Failed to scan %s: %v", hashKey, err)
			break
		}

		// HSCAN returns key-value pairs as a flat list: [key1, val1, key2, val2, ...]
		// Extract keys and values
		for i := 0; i < len(keys); i += 2 {
			if i+1 < len(keys) {
				values = append(values, keys[i+1])
				keys[i/2] = keys[i]
			}
		}
		keys = keys[:len(values)]

		// Check each entry's age and delete if stale
		toDelete := []string{}
		for i, jsonData := range values {
			// Parse JSON to check 'updated' timestamp
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
				continue // Skip malformed data
			}

			// Check 'updated' field (for pokestops/gyms/stations/routes) or 'last_seen' (for spawnpoints)
			var timestamp int64
			if updated, ok := data["updated"].(float64); ok {
				timestamp = int64(updated)
			} else if lastSeen, ok := data["last_seen"].(float64); ok {
				timestamp = int64(lastSeen)
			} else {
				continue // No timestamp field, keep it
			}

			// If older than cutoff, mark for deletion
			if timestamp < cutoffTime {
				toDelete = append(toDelete, keys[i])
			}
		}

		// Batch delete stale entries
		if len(toDelete) > 0 {
			if err := redisClient.HDel(ctx, hashKey, toDelete...).Err(); err != nil {
				log.Errorf("Failed to delete stale entries from %s: %v", hashKey, err)
			} else {
				deleted += len(toDelete)
			}
		}

		// If cursor is 0, we've scanned all entries
		if cursor == 0 {
			break
		}
	}

	if deleted > 0 {
		log.Infof("Trimmed %d stale entries from %s", deleted, hashKey)
	}

	return deleted
}

func init() {
	// Set GOMAXPROCS for optimal parallel performance
	if runtime.GOMAXPROCS(0) < 2 {
		runtime.GOMAXPROCS(2)
	}
}
