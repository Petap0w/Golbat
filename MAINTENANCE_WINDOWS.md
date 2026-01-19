# Handling Maintenance Windows

## The Scenario

You need to pause scanners for maintenance (e.g., 2 hours):
- Scanners stop sending data to Golbat
- No new spawnpoint sightings
- No new pokestop/gym updates
- Redis TTL continues ticking

## What Happens to Cache

### With Default TTL (60 minutes)

```
Timeline:
00:00 ├─ Scanners STOP
      │  Redis: Full cache (39.7M spawnpoints)
      │  L1 RAM: Full cache
      │
01:00 ├─ Redis TTL expires (60 min since last update)
      │  Redis: Data starts AUTO-DELETING
      │  L1 RAM: Still populated (if Golbat running)
      │
02:00 ├─ Scanners RESUME
      │  Redis: EMPTY or sparse
      │  L1 RAM: Still there (if Golbat didn't restart)
      │
      └─ Result: Cold start situation ⚠️
```

### With Recommended TTL (180 minutes)

```
Timeline:
00:00 ├─ Scanners STOP
      │  Redis: Full cache
      │  L1 RAM: Full cache
      │
01:00 │  Redis: Still full (60 min < 180 min TTL)
      │  L1 RAM: Still full
      │
02:00 ├─ Scanners RESUME (2-hour maintenance)
      │  Redis: Still full! ✅
      │  L1 RAM: Still full! ✅
      │
      └─ Result: No cold start, instant resume! 🎉
```

## Recommended Configurations

### Option 1: Standard Maintenance (< 3 hours) ✅ RECOMMENDED

**Config**:
```toml
[redis]
cache_ttl_minutes = 180  # 3 hours
load_hot_on_startup = true
```

**Best for**:
- Regular maintenance (1-2 hours)
- Scanner restarts
- Database updates
- Network issues

**Behavior**:
- Cache survives up to 3-hour pause
- Instant resume when scanners restart
- No performance hit

### Option 2: Extended Maintenance (3-6 hours)

**Config**:
```toml
[redis]
cache_ttl_minutes = 360  # 6 hours
load_hot_on_startup = true
```

**Best for**:
- Long maintenance windows
- Major infrastructure updates
- Very infrequent pauses

**Trade-off**:
- Uses slightly more Redis memory (stale data lingers longer)
- Still acceptable with 100GB Redis capacity

### Option 3: Very Long Pause (> 6 hours)

**Accept the reload**:
- Cache will expire
- `load_hot_on_startup = true` reloads on resume
- Takes 3-5 minutes (much better than old system!)

## Maintenance Procedures

### Procedure 1: Keep Golbat Running (BEST)

```bash
# 1. Stop scanners
pm2 stop scanner-workers

# 2. Keep Golbat running (important!)
pm2 list | grep golbat
# golbat        ✓ online
# golbat-writer ✓ online

# 3. Do your maintenance
# ... (database updates, server work, etc.)

# 4. Restart scanners
pm2 start scanner-workers

# Result: Zero cache loss! ✅
```

**Why this works**:
- L1 cache (RAM) stays populated in Golbat process
- Redis cache preserved (no expiration if < TTL)
- Instant resume

### Procedure 2: Restart Everything

```bash
# 1. Stop scanners
pm2 stop scanner-workers

# 2. Stop Golbat
pm2 stop golbat golbat-writer

# 3. Do maintenance
# ... 

# 4. Start Golbat first
pm2 start golbat golbat-writer

# 5. Wait for cache load (watch logs)
pm2 logs golbat --lines 50
# Look for: "Loaded 39741144 hot spawnpoints to Redis"
# Takes: 3-5 minutes

# 6. Start scanners
pm2 start scanner-workers

# Result: 3-5 minute delay, then full speed ✅
```

### Procedure 3: Emergency Restart (Redis Down)

```bash
# If Redis is down/corrupted:

# 1. Stop everything
pm2 stop all

# 2. Restart Redis
docker compose -f docker-compose.redis.yml restart

# 3. Start Golbat
pm2 start golbat golbat-writer

# 4. Wait for hot load (3-5 min)
# 5. Start scanners

# Result: Clean slate, full reload ✅
```

## Comparison: Before vs After

### Before Redis Refactor

```
2-hour scanner pause:
├─ Resume scanners
├─ Golbat queries DB for EVERY spawnpoint
│  └─ 39.7M individual queries
│  └─ Database: OVERLOADED
│  └─ "context deadline exceeded" errors
│  └─ Takes: 15+ minutes to recover
└─ Result: Extended downtime ❌
```

### After Redis Refactor (TTL = 180 min)

```
2-hour scanner pause (< 3 hours):
├─ Resume scanners
├─ Golbat reads from Redis (still cached!)
│  └─ 0 database queries needed
│  └─ Database: Normal load
│  └─ No errors
│  └─ Takes: 0 seconds (instant)
└─ Result: Instant resume ✅
```

### After Redis Refactor (> 3 hours, cache expired)

```
6-hour scanner pause:
├─ Resume scanners
├─ Golbat detects empty Redis
├─ Runs hot data load
│  ├─ 39.7M spawnpoints from DB
│  ├─ 3.5M pokestops from DB
│  ├─ Uses batch queries (not individual!)
│  └─ Takes: 3-5 minutes
└─ Result: Quick recovery ✅
```

## Monitoring During Maintenance

### Before Maintenance

```bash
# Check cache size
docker exec -it golbat-redis redis-cli INFO memory | grep used_memory_human
# Expected: ~30GB

# Check queue is empty
docker exec -it golbat-redis redis-cli XLEN golbat_writes:critical
# Expected: 0-100 items

# Save current state
docker exec -it golbat-redis redis-cli SAVE
```

### During Maintenance

```bash
# Monitor Redis memory (should stay stable)
docker exec -it golbat-redis redis-cli INFO memory | grep used_memory_human

# Check if Golbat is still running
pm2 status
```

### After Maintenance

```bash
# Watch cache reload (if needed)
pm2 logs golbat --lines 100 | grep -i "loading\|loaded"

# Verify Redis population
docker exec -it golbat-redis redis-cli DBSIZE
# Expected: Millions of keys

# Check queue processing
docker exec -it golbat-redis redis-cli XLEN golbat_writes:critical
# Should start growing then draining
```

## Memory Considerations

### Why TTL = 180 min is Safe

Your Redis has **100GB capacity**, currently using **~30GB**:

```
Memory usage by TTL:
├─ 60 min TTL:  ~30GB (active data only)
├─ 180 min TTL: ~32GB (active + recent)
├─ 360 min TTL: ~35GB (active + warm)
└─ Headroom: 65-70GB available ✅
```

The extra memory used by longer TTL is minimal because:
- **Active data** (scanned regularly) → Same size regardless of TTL
- **Inactive data** lingers a bit longer → Small increase
- **Dead data** eventually expires → No permanent buildup

## Recommendations by Maintenance Frequency

| Maintenance Pattern | Recommended TTL | Why |
|---------------------|-----------------|-----|
| Daily (< 1 hour) | 120 min | Comfortable buffer |
| Weekly (1-2 hours) | 180 min | **Recommended** |
| Bi-weekly (2-3 hours) | 240 min | Extra safety |
| Monthly (3-6 hours) | 360 min | Long maintenance |
| Rare (> 6 hours) | 180 min | Accept reload |

## Emergency Scenarios

### Scenario: Redis Crash During Maintenance

```bash
# Redis container died
docker ps | grep golbat-redis
# (no output)

# Restart Redis
docker compose -f docker-compose.redis.yml up -d

# Check Golbat logs
pm2 logs golbat --lines 50
# Should show: "Failed to connect to Redis" (temporary)
# Then: "Connected to Redis"

# If load_hot_on_startup = true:
# Golbat automatically reloads cache ✅
```

### Scenario: Golbat Crash During Maintenance

```bash
# Golbat stopped unexpectedly
pm2 status | grep golbat
# golbat    ✗ stopped

# Restart
pm2 restart golbat

# Cache reload happens automatically
# (3-5 min with load_hot_on_startup = true)
```

### Scenario: Full System Restart

```bash
# Everything down (server reboot)

# 1. Start Redis first
docker compose -f docker-compose.redis.yml up -d

# 2. Wait for Redis ready
docker exec -it golbat-redis redis-cli ping
# PONG

# 3. Start Golbat
pm2 start ecosystem.config.js

# 4. Wait for cache load (3-5 min)

# 5. Start scanners
pm2 start scanner-workers
```

## Best Practices

1. **Always keep Golbat running** during scanner maintenance if possible
2. **Set TTL = 180 min** (3 hours) for weekly maintenance windows
3. **Enable load_hot_on_startup = true** for automatic recovery
4. **Monitor Redis memory** to ensure TTL doesn't cause issues
5. **Test your maintenance procedure** before critical updates

## TL;DR

**Your Concern**: 
> "2-hour pause → Redis expires → back to old problem?"

**Answer**:
> ❌ NO! Here's why:
> - Set TTL = 180 min → Cache survives 3-hour pause
> - Keep Golbat running → L1 cache preserved
> - If cache expires → Reloads in 3-5 min (not hours!)
> - New architecture handles cold starts efficiently

**Recommended Config**:
```toml
[redis]
cache_ttl_minutes = 180  # Survives maintenance windows
load_hot_on_startup = true  # Auto-reloads if needed
```

**Result**: Maintenance windows are no longer a problem! ✅

