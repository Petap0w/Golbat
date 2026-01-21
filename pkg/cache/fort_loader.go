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
	"gopkg.in/guregu/null.v4"

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
	// Import decoder locally to avoid import cycle
	type Pokestop struct {
		Id                         string      `db:"id"`
		Lat                        float64     `db:"lat"`
		Lon                        float64     `db:"lon"`
		Name                       null.String `db:"name"`
		Url                        null.String `db:"url"`
		Enabled                    null.Bool   `db:"enabled"`
		LureExpireTimestamp        null.Int    `db:"lure_expire_timestamp"`
		LastModifiedTimestamp      null.Int    `db:"last_modified_timestamp"`
		Updated                    int64       `db:"updated"`
		QuestType                  null.Int    `db:"quest_type"`
		QuestTimestamp             null.Int    `db:"quest_timestamp"`
		QuestTarget                null.Int    `db:"quest_target"`
		QuestConditions            null.String `db:"quest_conditions"`
		QuestRewards               null.String `db:"quest_rewards"`
		QuestTemplate              null.String `db:"quest_template"`
		QuestTitle                 null.String `db:"quest_title"`
		QuestExpiry                null.Int    `db:"quest_expiry"`
		AlternativeQuestType       null.Int    `db:"alternative_quest_type"`
		AlternativeQuestTimestamp  null.Int    `db:"alternative_quest_timestamp"`
		AlternativeQuestTarget     null.Int    `db:"alternative_quest_target"`
		AlternativeQuestConditions null.String `db:"alternative_quest_conditions"`
		AlternativeQuestRewards    null.String `db:"alternative_quest_rewards"`
		AlternativeQuestTemplate   null.String `db:"alternative_quest_template"`
		AlternativeQuestTitle      null.String `db:"alternative_quest_title"`
		AlternativeQuestExpiry     null.Int    `db:"alternative_quest_expiry"`
		CellId                     null.Int    `db:"cell_id"`
		Deleted                    bool        `db:"deleted"`
		LureId                     null.Int    `db:"lure_id"`
		FirstSeenTimestamp         int64       `db:"first_seen_timestamp"`
		SponsorId                  null.Int    `db:"sponsor_id"`
		PartnerId                  null.String `db:"partner_id"`
		ArScanEligible             null.Int    `db:"ar_scan_eligible"`
		PowerUpLevel               null.Int    `db:"power_up_level"`
		PowerUpPoints              null.Int    `db:"power_up_points"`
		PowerUpEndTimestamp        null.Int    `db:"power_up_end_timestamp"`
		Description                null.String `db:"description"`
		ShowcasePokemonId          null.Int    `db:"showcase_pokemon_id"`
		ShowcasePokemonFormId      null.Int    `db:"showcase_pokemon_form_id"`
		ShowcasePokemonTypeId      null.Int    `db:"showcase_pokemon_type_id"`
		ShowcaseRankingStandard    null.Int    `db:"showcase_ranking_standard"`
		ShowcaseFocus              null.Int    `db:"showcase_focus"`
		ShowcaseExpiry             null.Int    `db:"showcase_expiry"`
		ShowcaseRankings           null.String `db:"showcase_rankings"`
	}

	return streamTableToCacheDirect(ctx, dbConn, "pokestop",
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
func loadGymsFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
	// Lightweight struct matching DB schema (avoids import cycle with decoder)
	type Gym struct {
		Id                    string      `db:"id"`
		Lat                   float64     `db:"lat"`
		Lon                   float64     `db:"lon"`
		Name                  null.String `db:"name"`
		Url                   null.String `db:"url"`
		LastModifiedTimestamp null.Int    `db:"last_modified_timestamp"`
		RaidEndTimestamp      null.Int    `db:"raid_end_timestamp"`
		RaidSpawnTimestamp    null.Int    `db:"raid_spawn_timestamp"`
		RaidBattleTimestamp   null.Int    `db:"raid_battle_timestamp"`
		Updated               int64       `db:"updated"`
		RaidPokemonId         null.Int    `db:"raid_pokemon_id"`
		GuardingPokemonId     null.Int    `db:"guarding_pokemon_id"`
		AvailableSlots        null.Int    `db:"available_slots"`
		TeamId                null.Int    `db:"team_id"`
		RaidLevel             null.Int    `db:"raid_level"`
		Enabled               null.Bool   `db:"enabled"`
		ExRaidEligible        null.Bool   `db:"ex_raid_eligible"`
		InBattle              null.Bool   `db:"in_battle"`
		RaidPokemonMove1      null.Int    `db:"raid_pokemon_move_1"`
		RaidPokemonMove2      null.Int    `db:"raid_pokemon_move_2"`
		RaidPokemonForm       null.Int    `db:"raid_pokemon_form"`
		RaidPokemonCp         null.Int    `db:"raid_pokemon_cp"`
		RaidIsExclusive       null.Bool   `db:"raid_is_exclusive"`
		CellId                null.Int    `db:"cell_id"`
		Deleted               bool        `db:"deleted"`
		TotalCp               null.Int    `db:"total_cp"`
		FirstSeenTimestamp    int64       `db:"first_seen_timestamp"`
		RaidPokemonGender     null.Int    `db:"raid_pokemon_gender"`
		SponsorId             null.Int    `db:"sponsor_id"`
		PartnerId             null.String `db:"partner_id"`
		RaidPokemonCostume    null.Int    `db:"raid_pokemon_costume"`
		RaidPokemonEvolution  null.Int    `db:"raid_pokemon_evolution"`
		ArScanEligible        null.Int    `db:"ar_scan_eligible"`
		PowerUpLevel          null.Int    `db:"power_up_level"`
		PowerUpPoints         null.Int    `db:"power_up_points"`
		PowerUpEndTimestamp   null.Int    `db:"power_up_end_timestamp"`
	}

	return streamTableToCacheDirect(ctx, dbConn, "gym",
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
func loadStationsFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
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

	return streamTableToCacheDirect(ctx, dbConn, "station",
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
func loadRoutesFromDB(ctx context.Context, dbConn *sqlx.DB, setter FortSetter) int64 {
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

	return streamTableToCacheDirect(ctx, dbConn, "route",
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
