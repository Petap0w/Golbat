# Redis Settings for 10k/sec Scale

This document explains the Redis configuration optimized for Golbat at 10,000 decodes/second.

## Your Scale

```
Production:
- 10,000 decodes/second
- 3.5M pokestops
- ~1M gyms
- 39.7M active spawnpoints (7 days)
- 512GB RAM server
```

## Critical Settings Explained

### 1. Client Timeouts (pkg/redis/client.go)

```go
ReadTimeout:  500 * time.Millisecond  // Fast cache operations
WriteTimeout: 1 * time.Second          // Queue writes
PoolTimeout:  2 * time.Second          // Connection pool
```

**Why these values?**
- **READ operations should be FAST** (1-5ms normally)
- Even during BGSAVE, reads should be <50ms
- 500ms timeout catches actual problems quickly
- If reads take >500ms, something is **broken**

**The real fix for BGSAVE slowdown:**
- Reduce BGSAVE frequency (every 15-30 min, not every minute)
- With writer working properly, queues stay small
- Redis stays responsive even during BGSAVE

**Performance expectations:**
- Normal: 1-5ms per operation
- During BGSAVE: 10-50ms per operation
- **Never:** seconds!

**If you see timeouts with these settings:**
→ Investigate Redis performance, don't just increase timeout!

### 2. RDB Save Policy (docker-compose.redis.yml)

```yaml
--save 3600 1      # Every hour if 1+ change
--save 1800 100    # Every 30 min if 100+ changes  
--save 900 1000    # Every 15 min if 1000+ changes
```

**Old settings (WRONG):**
```yaml
--save 60 10000    # Every minute if 10k changes
# → At 10k/sec, you hit this INSTANTLY!
# → BGSAVE every minute = constant blocking
```

**Why new settings work:**
- You already have AOF (appendonly yes) for durability
- RDB snapshots are just for faster restarts
- Hourly/30-min saves are sufficient
- Reduces blocking operations by 95%

### 3. Memory Policy

```yaml
--maxmemory 100gb
--maxmemory-policy allkeys-lru   # Changed from noeviction
```

**Why allkeys-lru?**
- `noeviction` → Redis blocks writes when full
- `allkeys-lru` → Evicts least-recently-used keys
- For 100M spawnpoints, we CAN'T fit everything
- Hot/cold separation relies on LRU eviction
- Your data naturally re-populates on access

**Expected Memory Usage:**
```
Pokestops:    3.5M × ~1KB  = ~3.5GB
Gyms:         1M × ~2KB    = ~2GB
Spawnpoints:  39.7M × 0.5KB = ~20GB (hot only)
Queues:       ~2-5GB
Total:        ~30-35GB
```

### 4. Connection Settings

```yaml
--tcp-backlog 4096       # Was 511 - increased for high connections
--maxclients 50000       # Added - handles many Golbat workers
```

**Why?**
- Default backlog (511) too small for 10k/sec
- Each Golbat worker needs Redis connections
- Pool size of 200 × potential multiple instances
- 4096 backlog prevents connection queue overflow

### 5. AOF Settings (Already Correct)

```yaml
--appendonly yes
--appendfsync everysec
```

**This is your MAIN durability mechanism:**
- Logs every write to disk
- `everysec` = fsync every second (fast + safe)
- Provides point-in-time recovery
- RDB snapshots are just for faster restarts

### 6. Error Handling

```yaml
--stop-writes-on-bgsave-error no
```

**Why?**
- Don't halt writes if RDB save fails
- AOF is your primary durability
- RDB failure shouldn't impact operations
- Monitor for RDB failures separately

## Settings by Deployment Type

### Development (Low Volume)
```yaml
--maxmemory 10gb
--save 3600 1
--maxclients 1000
```

### Production (10k/sec)
```yaml
--maxmemory 100gb
--save 3600 1 1800 100 900 1000
--maxclients 50000
--tcp-backlog 4096
```

## Performance Calculations

### Old Settings Problem:
```
10,000 decodes/sec × 60 sec = 600,000 writes/min
Trigger: --save 60 10000
Result: BGSAVE every minute!
Each BGSAVE: 12 seconds
Client timeout: 3 seconds
Outcome: ❌ Constant timeouts
```

### New Settings:
```
10,000 decodes/sec × 900 sec = 9,000,000 writes/15min
Trigger: --save 900 1000
Result: BGSAVE every 15 minutes
Each BGSAVE: 12 seconds
Client timeout: 30 seconds
Outcome: ✅ No timeouts
```

## Monitoring Commands

Check if settings are active:
```bash
docker exec -it golbat-redis redis-cli -a PASSWORD CONFIG GET save
docker exec -it golbat-redis redis-cli -a PASSWORD CONFIG GET maxmemory-policy
docker exec -it golbat-redis redis-cli -a PASSWORD INFO stats
```

Monitor BGSAVE operations:
```bash
docker logs golbat-redis | grep "Background saving"
```

Check memory usage:
```bash
docker exec -it golbat-redis redis-cli -a PASSWORD INFO memory
```

## Quick Fix Summary

**If you still see timeouts after these changes:**

1. Check Redis is not hitting maxmemory:
```bash
docker exec -it golbat-redis redis-cli -a PASSWORD INFO memory | grep maxmemory
```

2. Check queue sizes aren't growing:
```bash
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical
```

3. Verify writer is processing:
```bash
pm2 logs golbat-writer --lines 50
```

## Why This Works

1. **30-second timeouts** → Survive BGSAVE operations
2. **Relaxed save policy** → BGSAVE every 15-30 min instead of every minute
3. **allkeys-lru** → Graceful degradation when full, not blocking
4. **High connection limits** → Handle your worker scale
5. **AOF durability** → Don't rely on RDB for safety

Your initial timeout was a **mismatch between save frequency and client timeout**, not a fundamental architecture issue.

