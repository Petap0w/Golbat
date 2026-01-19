# Complete Fix Guide for Golbat Redis Refactor

## Summary of Issues Found

### Issue #1: Writer Batch Failures (FIXED ✅)
**Problem:** When BatchUpsert failed on ANY item in a batch of 1000, ALL 1000 messages stayed PENDING forever.
**Solution:** Process items individually, ACK only successful ones.
**Status:** All 10 writer functions fixed in `pkg/writer/db_writer.go`

### Issue #2: Missing Queue Calls (IN PROGRESS ⏳)
**Problem:** Only pokestops, gyms, and spawnpoints are queued to Redis. All other data types (incidents, tappables, weather, stations, routes, s2cells, players) go directly to the database, bypassing Redis entirely!
**Solution:** Refactor all `save*Record()` functions to use `queueWrite()` like pokestops and gyms do.

## Remaining Work

### Data Types That Need Fixing:

1. ✅ **Incidents** - FIXED in `decoder/incident.go`
2. ❌ **Tappables** - `decoder/tappable.go` line 155
3. ❌ **Weather** - `decoder/weather.go` line 158
4. ❌ **Stations** - `decoder/station.go` line 76
5. ❌ **Routes** - `decoder/routes.go` line 91
6. ❌ **Players** - `decoder/player.go` line 339

### Pattern to Apply

For each `save*Record()` function, replace direct DB writes with:

```go
func save*Record(ctx context.Context, db db.DbDetails, item *Type) {
    // ... existing checks ...
    
    item.Updated = time.Now().Unix()

    // Update L1 cache immediately for read consistency
    *Cache.Set(item.Id, *item, ttlcache.DefaultTTL)

    // Queue write to database
    if redisEnabled {
        if err := queueWrite(ctx, "typename", "upsert", item); err != nil {
            log.Warnf("Failed to queue * write for %s: %s", item.Id, err)
            // Fall back to direct DB write
            save*RecordDirect(ctx, db, item)
        }
    } else {
        // Direct DB write if Redis not enabled
        save*RecordDirect(ctx, db, item)
    }

    // ... existing webhooks ...
}

// save*RecordDirect writes directly to DB (fallback or no-Redis mode)
func save*RecordDirect(ctx context.Context, db db.DbDetails, item *Type) {
    res, err := db.GeneralDb.NamedExecContext(ctx,
        `INSERT INTO * (...) VALUES (...) ON DUPLICATE KEY UPDATE ...`,
        item)

    statsCollector.IncDbQuery("upsert *", err)
    if err != nil {
        log.Errorf("upsert * %s: %s", item.Id, err)
    }
    _ = res
}
```

### Example: Tappables

File: `decoder/tappable.go` line 155

**Current code:**
```go
func saveTappableRecord(ctx context.Context, details db.DbDetails, tappable *Tappable) {
    oldTappable, _ := GetTappableRecord(ctx, details, tappable.Id)
    now := time.Now().Unix()
    if oldTappable != nil && !hasChangesTappable(oldTappable, tappable) {
        return
    }
    tappable.Updated = now
    if oldTappable == nil {
        res, err := details.GeneralDb.NamedExecContext(ctx, fmt.Sprintf(`
            INSERT INTO tappable (...)
            VALUES ("%d", :lat, :lon, ...)
            `, tappable.Id), tappable)
        // ... error handling ...
    } else {
        res, err := details.GeneralDb.NamedExecContext(ctx, fmt.Sprintf(`
            UPDATE tappable SET ...
            WHERE id = "%d"
            `, tappable.Id), tappable)
        // ... error handling ...
    }
    tappableCache.Set(tappable.Id, *tappable, ttlcache.DefaultTTL)
}
```

**Should become:**
```go
func saveTappableRecord(ctx context.Context, details db.DbDetails, tappable *Tappable) {
    oldTappable, _ := GetTappableRecord(ctx, details, tappable.Id)
    now := time.Now().Unix()
    if oldTappable != nil && !hasChangesTappable(oldTappable, tappable) {
        return
    }
    tappable.Updated = now

    // Update L1 cache immediately
    tappableCache.Set(tappable.Id, *tappable, ttlcache.DefaultTTL)

    // Queue write to database
    if redisEnabled {
        if err := queueWrite(ctx, "tappable", "upsert", tappable); err != nil {
            log.Warnf("Failed to queue tappable write for %d: %s", tappable.Id, err)
            saveTappableRecordDirect(ctx, details, tappable)
        }
    } else {
        saveTappableRecordDirect(ctx, details, tappable)
    }
}

func saveTappableRecordDirect(ctx context.Context, details db.DbDetails, tappable *Tappable) {
    res, err := details.GeneralDb.NamedExecContext(ctx, fmt.Sprintf(`
        INSERT INTO tappable (
            id, lat, lon, fort_id, spawn_id, type, pokemon_id, item_id, count, expire_timestamp, expire_timestamp_verified, updated
        ) VALUES (
            "%d", :lat, :lon, :fort_id, :spawn_id, :type, :pokemon_id, :item_id, :count, :expire_timestamp, :expire_timestamp_verified, :updated
        )
        ON DUPLICATE KEY UPDATE
            lat = VALUES(lat),
            lon = VALUES(lon),
            fort_id = VALUES(fort_id),
            spawn_id = VALUES(spawn_id),
            type = VALUES(type),
            pokemon_id = VALUES(pokemon_id),
            item_id = VALUES(item_id),
            count = VALUES(count),
            expire_timestamp = VALUES(expire_timestamp),
            expire_timestamp_verified = VALUES(expire_timestamp_verified),
            updated = VALUES(updated)
        `, tappable.Id), tappable)

    statsCollector.IncDbQuery("upsert tappable", err)
    if err != nil {
        log.Errorf("upsert tappable %d: %s", tappable.Id, err)
    }
    _ = res
}
```

## Deployment Steps

After completing all fixes:

### 1. Rebuild Both Binaries
```bash
cd /Users/jean/WebStorm/Golbat
go build -o golbat
go build -o golbat-writer cmd/golbat-writer/main.go
```

### 2. Deploy to Server
```bash
scp golbat jean@10.10.10.170:~/
scp golbat-writer jean@10.10.10.170:~/

ssh jean@10.10.10.170
pm2 stop golbat golbat-writer
cp ~/golbat /home/jean/
cp ~/golbat-writer /home/jean/
```

### 3. Clear PENDING Messages
```bash
docker exec golbat-redis redis-cli -a TISuaIsyGOjP0bleQKc XGROUP SETID golbat_writes:critical golbat-writers 0
```

### 4. Restart Services
```bash
pm2 restart golbat golbat-writer
pm2 logs --lines 200
```

### 5. Monitor Queue
```bash
watch -n 2 'docker exec golbat-redis redis-cli -a TISuaIsyGOjP0bleQKc XLEN golbat_writes:critical'
```

**Expected Result:** Queue should drain rapidly and stay at 0 or small numbers (<1000).

## Files Modified

- ✅ `pkg/writer/db_writer.go` - All 10 process functions fixed
- ✅ `decoder/incident.go` - Added queueWrite
- ⏳ `decoder/tappable.go` - Needs queueWrite
- ⏳ `decoder/weather.go` - Needs queueWrite
- ⏳ `decoder/station.go` - Needs queueWrite
- ⏳ `decoder/routes.go` - Needs queueWrite
- ⏳ `decoder/player.go` - Needs queueWrite

## Testing

After deployment, you should see in writer logs:
- "Processed batch of X tappables"
- "Processed batch of X weather records"
- "Processed batch of X stations"
- "Processed batch of X routes"
- "Processed batch of X players"

If you don't see these, those data types are still not being queued!

