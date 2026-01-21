package decoder

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/jellydator/ttlcache/v3"
	log "github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v4"

	"golbat/config"
	"golbat/db"
	"golbat/pogo"
)

// Spawnpoint struct.
// REMINDER! Keep hasChangesSpawnpoint updated after making changes
type Spawnpoint struct {
	Id         int64    `db:"id"`
	Lat        float64  `db:"lat"`
	Lon        float64  `db:"lon"`
	Updated    int64    `db:"updated"`
	LastSeen   int64    `db:"last_seen"`
	DespawnSec null.Int `db:"despawn_sec"`
}

//CREATE TABLE `spawnpoint` (
//`id` bigint unsigned NOT NULL,
//`lat` double(18,14) NOT NULL,
//`lon` double(18,14) NOT NULL,
//`updated` int unsigned NOT NULL DEFAULT '0',
//`last_seen` int unsigned NOT NULL DEFAULT '0',
//`despawn_sec` smallint unsigned DEFAULT NULL,
//PRIMARY KEY (`id`),
//KEY `ix_coords` (`lat`,`lon`),
//KEY `ix_updated` (`updated`),
//KEY `ix_last_seen` (`last_seen`)
//)

func getSpawnpointRecord(ctx context.Context, db db.DbDetails, spawnpointId int64) (*Spawnpoint, error) {
	// L1 cache check
	inMemorySpawnpoint := spawnpointCache.Get(spawnpointId)
	if inMemorySpawnpoint != nil {
		spawnpoint := inMemorySpawnpoint.Value()
		return &spawnpoint, nil
	}

	// L2 cache check (Redis) - using optimized batch loader
	if redisEnabled && spawnpointBatch != nil {
		records, err := spawnpointBatch.BatchLoad(ctx, []int64{spawnpointId})
		if err == nil {
			if record, found := records[spawnpointId]; found {
				spawnpoint := &Spawnpoint{
					Id:         record.Id,
					Lat:        record.Lat,
					Lon:        record.Lon,
					DespawnSec: null.IntFromPtr(record.DespawnSec),
					Updated:    record.Updated,
					LastSeen:   record.LastSeen,
				}
				return spawnpoint, nil
			}
		}
	}

	// DB fallback
	spawnpoint := Spawnpoint{}
	err := db.GeneralDb.GetContext(ctx, &spawnpoint, "SELECT id, lat, lon, updated, last_seen, despawn_sec FROM spawnpoint WHERE id = ?", spawnpointId)

	statsCollector.IncDbQuery("select spawnpoint", err)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return &Spawnpoint{Id: spawnpointId}, err
	}

	// Populate caches
	spawnpointCache.Set(spawnpointId, spawnpoint, ttlcache.DefaultTTL)
	if redisEnabled && l2Cache != nil {
		l2Cache.SetSpawnpoint(ctx, spawnpoint.Id, spawnpoint.Lat, spawnpoint.Lon,
			spawnpoint.DespawnSec.ValueOrZero(), spawnpoint.Updated, spawnpoint.LastSeen)
	}

	return &spawnpoint, nil
}

func Abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func hasChangesSpawnpoint(old *Spawnpoint, new *Spawnpoint) bool {
	if !floatAlmostEqual(old.Lat, new.Lat, floatTolerance) ||
		!floatAlmostEqual(old.Lon, new.Lon, floatTolerance) ||
		(old.DespawnSec.Valid && !new.DespawnSec.Valid) ||
		(!old.DespawnSec.Valid && new.DespawnSec.Valid) {
		return true
	}
	if !old.DespawnSec.Valid && !new.DespawnSec.Valid {
		return false
	}

	// Ignore small movements in despawn time
	oldDespawnSec := old.DespawnSec.Int64
	newDespawnSec := new.DespawnSec.Int64

	if oldDespawnSec <= 1 && newDespawnSec >= 3598 {
		return false
	}
	if newDespawnSec <= 1 && oldDespawnSec >= 3598 {
		return false
	}

	return Abs(old.DespawnSec.Int64-new.DespawnSec.Int64) > 2
}

func spawnpointUpdateFromWild(ctx context.Context, db db.DbDetails, wildPokemon *pogo.WildPokemonProto, timestampMs int64) {
	spawnId, err := strconv.ParseInt(wildPokemon.SpawnPointId, 16, 64)
	if err != nil {
		panic(err)
	}

	if wildPokemon.TimeTillHiddenMs <= 90000 && wildPokemon.TimeTillHiddenMs > 0 {
		expireTimeStamp := (timestampMs + int64(wildPokemon.TimeTillHiddenMs)) / 1000

		date := time.Unix(expireTimeStamp, 0)
		secondOfHour := date.Second() + date.Minute()*60
		spawnpoint := Spawnpoint{
			Id:         spawnId,
			Lat:        wildPokemon.Latitude,
			Lon:        wildPokemon.Longitude,
			DespawnSec: null.IntFrom(int64(secondOfHour)),
		}
		spawnpointUpdate(ctx, db, &spawnpoint)
	} else {
		spawnPoint, _ := getSpawnpointRecord(ctx, db, spawnId)
		if spawnPoint == nil {
			spawnpoint := Spawnpoint{
				Id:  spawnId,
				Lat: wildPokemon.Latitude,
				Lon: wildPokemon.Longitude,
			}
			spawnpointUpdate(ctx, db, &spawnpoint)
		} else {
			spawnpointSeen(ctx, db, spawnId)
		}
	}
}

func spawnpointUpdate(ctx context.Context, db db.DbDetails, spawnpoint *Spawnpoint) {
	oldSpawnpoint, _ := getSpawnpointRecord(ctx, db, spawnpoint.Id)

	now := time.Now().Unix()
	if oldSpawnpoint != nil && !hasChangesSpawnpoint(oldSpawnpoint, spawnpoint) {
		// Force update after configured interval (default: 24 hours)
		// Confirms spawnpoint still exists even if spawn time unchanged
		forceUpdateInterval := config.Config.Tuning.GetForceUpdateInterval("spawnpoint")
		if oldSpawnpoint.Updated > now-forceUpdateInterval {
			return
		}
	}

	spawnpoint.Updated = time.Now().Unix()
	spawnpoint.LastSeen = time.Now().Unix()

	// Update L1 cache immediately
	spawnpointCache.Set(spawnpoint.Id, *spawnpoint, ttlcache.DefaultTTL)

	// Update L2 cache immediately (optimized format)
	if redisEnabled && l2Cache != nil {
		l2Cache.SetSpawnpoint(ctx, spawnpoint.Id, spawnpoint.Lat, spawnpoint.Lon,
			spawnpoint.DespawnSec.ValueOrZero(), spawnpoint.Updated, spawnpoint.LastSeen)
	}

	// Queue write to database
	if redisEnabled {
		if err := queueWrite(ctx, "spawnpoint", "upsert", spawnpoint); err != nil {
			log.Warnf("Failed to queue spawnpoint write for %d: %s", spawnpoint.Id, err)
			// Fall back to direct DB write
			spawnpointUpdateDirect(ctx, db, spawnpoint)
		}
	} else {
		// Direct DB write if Redis not enabled
		spawnpointUpdateDirect(ctx, db, spawnpoint)
	}
}

// spawnpointUpdateDirect writes directly to DB (fallback or no-Redis mode)
func spawnpointUpdateDirect(ctx context.Context, db db.DbDetails, spawnpoint *Spawnpoint) {
	_, err := db.GeneralDb.NamedExecContext(ctx, "INSERT INTO spawnpoint (id, lat, lon, updated, last_seen, despawn_sec)"+
		"VALUES (:id, :lat, :lon, :updated, :last_seen, :despawn_sec)"+
		"ON DUPLICATE KEY UPDATE "+
		"lat=VALUES(lat),"+
		"lon=VALUES(lon),"+
		"updated=VALUES(updated),"+
		"last_seen=VALUES(last_seen),"+
		"despawn_sec=VALUES(despawn_sec)", spawnpoint)

	statsCollector.IncDbQuery("insert spawnpoint", err)
	if err != nil {
		log.Errorf("Error updating spawnpoint %s", err)
	}
}

func spawnpointSeen(ctx context.Context, db db.DbDetails, spawnpointId int64) {
	inMemorySpawnpoint := spawnpointCache.Get(spawnpointId)
	if inMemorySpawnpoint == nil {
		// This should never happen, since all routes here have previously created a spawnpoint in the cache
		return
	}

	spawnpoint := inMemorySpawnpoint.Value()
	now := time.Now().Unix()

	// Only update last_seen once per day (86400 seconds = 24 hours)
	// This reduces unnecessary DB writes for active spawnpoints
	if now-spawnpoint.LastSeen > 86400 {
		spawnpoint.LastSeen = now

		// Queue write to database if Redis enabled
		if redisEnabled {
			// Update cache immediately
			spawnpointCache.Set(spawnpoint.Id, spawnpoint, ttlcache.DefaultTTL)
			if l2Cache != nil {
				l2Cache.SetSpawnpoint(ctx, spawnpoint.Id, spawnpoint.Lat, spawnpoint.Lon,
					spawnpoint.DespawnSec.ValueOrZero(), spawnpoint.Updated, spawnpoint.LastSeen)
			}

			// Queue the write
			if err := queueWrite(ctx, "spawnpoint", "upsert", &spawnpoint); err != nil {
				log.Warnf("Failed to queue spawnpoint last_seen update: %s", err)
				// Fall back to direct write
				spawnpointSeenDirect(ctx, db, now, spawnpointId)
			}
		} else {
			// Direct DB write if Redis not enabled
			spawnpointSeenDirect(ctx, db, now, spawnpointId)
		}

		spawnpointCache.Set(spawnpoint.Id, spawnpoint, ttlcache.DefaultTTL)
	}
}

// spawnpointSeenDirect performs direct DB update (fallback or no-Redis mode)
func spawnpointSeenDirect(ctx context.Context, db db.DbDetails, now int64, spawnpointId int64) {
	_, err := db.GeneralDb.ExecContext(ctx, "UPDATE spawnpoint "+
		"SET last_seen=? "+
		"WHERE id = ? ", now, spawnpointId)
	statsCollector.IncDbQuery("update spawnpoint", err)
	if err != nil {
		log.Printf("Error updating spawnpoint last seen %s", err)
	}
}
