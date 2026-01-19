# Memory Usage Guide - Before vs After Redis Refactor

## Current State (Before Redis)

Your Golbat currently uses **~55GB RAM**:

```
Golbat Process Memory Breakdown:
├─ L1 Cache (all data in RAM):
│  ├─ Spawnpoints: ~20-25GB (39.7M active)
│  ├─ Pokestops: ~7-8GB (3.5M)
│  ├─ Gyms: ~2-3GB (1M)
│  ├─ Pokemon cache: ~5-10GB
│  ├─ Weather/Incidents: ~2-3GB
│  └─ Other data: ~3-5GB
│
├─ Application overhead: ~5-10GB
│  ├─ Go runtime
│  ├─ Goroutines
│  └─ Internal buffers
│
└─ Total: ~55GB
```

## Expected State (After Redis)

### Memory Distribution

With Redis refactor, memory is **split** between two processes:

```
┌─────────────────────────────────────────────────────────┐
│ SERVER MEMORY (assume 128GB total)                      │
├─────────────────────────────────────────────────────────┤
│                                                          │
│ Golbat Process:           35-45GB ⬇️ (down from 55GB)  │
│ ├─ L1 Cache (hot data):   25-30GB                      │
│ ├─ Application:           10-15GB                      │
│ └─ PM2 limit set to:      60GB (safety buffer)         │
│                                                          │
│ Redis Container:          30-35GB 🆕                    │
│ ├─ Spawnpoints:           ~20GB                         │
│ ├─ Pokestops:             ~7GB                          │
│ ├─ Gyms:                  ~2GB                          │
│ ├─ Other data:            ~1-2GB                        │
│ └─ Overhead:              ~1-3GB                        │
│                                                          │
│ golbat-writer:            2-4GB 🆕                      │
│ └─ PM2 limit set to:      4GB                           │
│                                                          │
│ System + Other:           ~20GB                         │
│                                                          │
│ TOTAL EXPECTED:           67-84GB                       │
│ (vs 55GB before, but more efficient!)                   │
└─────────────────────────────────────────────────────────┘
```

## Why Total Memory Might Increase Initially

**Before Redis**: Everything in one process (55GB)
**After Redis**: Split across processes (67-84GB)

### Reasons:

1. **Dual Caching During Transition**:
   ```
   - L1 still caches frequently accessed data
   - L2 (Redis) caches all data
   - Some overlap during normal operation
   ```

2. **Redis Overhead**:
   ```
   - Redis data structures (Hashes, Streams)
   - Persistence (AOF buffering)
   - Connection overhead
   ```

3. **Write Queue**:
   ```
   - Redis Streams hold pending writes
   - Can grow to 1M items = ~100-500MB
   ```

## Expected Memory Evolution

### Phase 1: Initial Deployment (Day 1-3)

```
Golbat:     50-55GB  (still learning what to keep in L1)
Redis:      30-35GB  (all data cached)
Writer:     2-4GB
Total:      82-94GB
```

**Why**: L1 cache still aggressive, gradually learning

### Phase 2: Optimization (Week 1-2)

```
Golbat:     40-45GB  (L1 cache optimized)
Redis:      30-35GB  (stable)
Writer:     2-4GB
Total:      72-84GB
```

**Why**: L1 TTL working, only hot data cached

### Phase 3: Steady State (Week 2+)

```
Golbat:     35-45GB  (optimal L1 size)
Redis:      30-35GB  (stable)
Writer:     2-4GB
Total:      67-84GB
```

**Why**: System learned access patterns

## PM2 Configuration Explained

```javascript
{
  name: 'golbat',
  max_memory_restart: '60G',  // Set to 60GB
  // Current: 55GB
  // Expected after optimization: 35-45GB
  // Buffer: 15-25GB for safety
}
```

### Why 60GB Limit:

1. **Current usage**: 55GB
2. **Initial phase**: Might stay at 50-55GB temporarily
3. **Safety buffer**: 5-10GB headroom
4. **Prevents runaway**: Restart if memory leak

### Monitoring & Adjustment:

After 1-2 weeks of operation:

```bash
# Check actual Golbat memory usage
pm2 status
# Look at "memory" column for golbat

# If stable at 40GB, can reduce limit to 50GB
# If growing beyond 55GB, investigate
```

## Memory Optimization Settings

### L1 Cache TTL (Golbat)

Currently uses `ttlcache.DefaultTTL`:

```go
spawnpointCache.Set(id, spawnpoint, ttlcache.DefaultTTL)
```

**Default is typically**: 1 hour

This means L1 cache **also** expires after 1 hour of no access, further reducing memory.

### L2 Cache TTL (Redis)

```toml
[redis]
cache_ttl_minutes = 120  # 2 hours as agreed
```

So data flow:
```
Active data:
├─ Accessed frequently
├─ Always in L1 (refreshed within 1 hour)
├─ Always in L2 (refreshed within 2 hours)
└─ Never hits DB ✅

Inactive data:
├─ Not accessed for 1+ hour
├─ Expires from L1 (freed from Golbat RAM)
├─ Still in L2 for 2 hours
├─ If accessed: L2→L1 (fast)
└─ After 2 hours: L2 expires, falls back to DB
```

## Expected Benefits Despite Higher Total Memory

### 1. Database Load ⬇️ 80-90%

```
Before: 55GB RAM + Heavy DB load
After:  67-84GB RAM + Minimal DB load
```

**Trade**: More RAM for much less DB stress ✅

### 2. Better Performance 📈

```
Before: Synchronous DB writes (blocking)
After:  Async queue writes (non-blocking)
```

**Trade**: More RAM for better throughput ✅

### 3. Stability 🎯

```
Before: Context deadline errors during load
After:  Smooth operation at 10k/sec
```

**Trade**: More RAM for reliability ✅

### 4. Scalability 🚀

```
Before: Single process limit
After:  Can run multiple Golbat instances sharing Redis
```

**Trade**: More RAM for horizontal scaling ✅

## If Memory is Constrained

If your server doesn't have 80-100GB available, options:

### Option 1: Reduce Redis Cache Scope

```toml
[redis]
load_hot_on_startup = false  # Don't preload everything
cache_ttl_minutes = 60        # Expire faster
```

**Effect**: 
- Redis: 15-20GB (instead of 30-35GB)
- More DB queries, but still much better than before

### Option 2: Adjust L1 TTL

Can modify L1 cache TTL to expire faster:
```go
// In decoder/main.go, change DefaultTTL
spawnpointCache = ttlcache.New[int64, Spawnpoint](
    ttlcache.WithTTL[int64, Spawnpoint](30 * time.Minute), // 30 min instead of 1 hour
)
```

**Effect**:
- Golbat: 30-35GB (instead of 40-45GB)
- Slightly more L2 cache hits

### Option 3: Smaller Writer Count

```toml
[redis]
writer_workers = 2  # Instead of 4
```

**Effect**:
- Writer: 1-2GB (instead of 2-4GB)
- Slightly slower DB writes (probably fine)

## Monitoring Commands

### Check Current Memory Usage

```bash
# PM2 dashboard
pm2 monit

# Detailed memory per process
pm2 status

# System memory
free -h

# Redis memory
docker exec -it golbat-redis redis-cli INFO memory | grep used_memory_human

# Breakdown
echo "Golbat: $(pm2 describe golbat | grep memory)"
echo "Writer: $(pm2 describe golbat-writer | grep memory)"
echo "Redis: $(docker stats --no-stream golbat-redis --format '{{.MemUsage}}')"
```

### Memory Alerts

Set up monitoring:
```bash
# Add to cron (check every 5 min)
*/5 * * * * /path/to/check-memory.sh

# check-memory.sh:
#!/bin/bash
GOLBAT_MEM=$(pm2 jlist | jq '.[0].monit.memory / 1024 / 1024 / 1024')
if (( $(echo "$GOLBAT_MEM > 58" | bc -l) )); then
    echo "WARNING: Golbat using ${GOLBAT_MEM}GB (approaching 60GB limit)"
fi
```

## Expected Timeline

```
Week 1:  Deploy, memory = 80-90GB (learning phase)
Week 2:  Optimize, memory = 70-80GB (settling)
Week 3+: Stable, memory = 67-75GB (optimal)
```

If memory stays above 80GB after 2 weeks, tune:
- L1 TTL shorter
- L2 TTL shorter  
- load_hot_on_startup = false

## Summary

| Metric | Before | After (Initial) | After (Optimized) |
|--------|--------|-----------------|-------------------|
| Golbat RAM | 55GB | 50-55GB | 35-45GB |
| Redis RAM | 0GB | 30-35GB | 30-35GB |
| Writer RAM | 0GB | 2-4GB | 2-4GB |
| **Total RAM** | **55GB** | **82-94GB** | **67-84GB** |
| DB Load | 100% | 20-30% | 10-20% |
| Throughput | Context errors | 10k/sec | 10k/sec |
| Stability | ⚠️ Issues | ✅ Stable | ✅ Stable |

**Bottom Line**: Trade some RAM for massive stability and performance improvements ✅

## PM2 Config Updated

```javascript
{
  name: 'golbat',
  max_memory_restart: '60G',  // ✅ Updated from 10G
  // Safe for current 55GB + headroom for optimization phase
}
```

You can monitor and adjust down to 50G after a few weeks if memory stabilizes lower.

