# Speed-First Approach - The Right Way

## The Core Principle

**Redis IS the fast path.** Operations must be in **milliseconds**, not seconds.

If Redis operations take seconds, something is **broken** and must be fixed, not masked with long timeouts.

## Correct Timeout Settings

### For Cache Reads (Critical Path)

```go
ReadTimeout: 500 * time.Millisecond
```

**Expected Performance:**
- **Normal:** 1-5ms
- **During BGSAVE:** 10-50ms  
- **Never:** >500ms

**If operations timeout at 500ms:**
→ **Investigate and fix**, don't increase timeout!

### For Queue Writes (Less Critical)

```go
WriteTimeout: 1 * time.Second
```

**Why longer?**
- Redis Streams writes can batch
- Less critical than reads (async path)
- Still fails fast at 1 second

## Why This Is Different from My Initial Fix

### What I Did Wrong Initially ❌

```go
ReadTimeout: 30 * time.Second  // WRONG!
```

**Reasoning:** "Redis BGSAVE takes 12 seconds, so we need 30s timeout"

**Problem:** This masks the real issue (Redis being slow) instead of fixing it.

### The Correct Approach ✅

**Step 1:** Fix the root causes
- ✅ Writer actually processes messages (queues don't grow)
- ✅ BGSAVE happens every 15-30 min (not every minute)
- ✅ allkeys-lru policy (graceful degradation)

**Step 2:** Set timeouts for FAST operations
- ✅ 500ms for reads (enforces speed requirement)
- ✅ 1s for writes (queue operations)

**Step 3:** Monitor and investigate timeouts
- Timeout at 500ms = **something is broken**, not "needs more time"
- Investigate: Redis CPU, memory, network, queue sizes
- Fix the problem, don't hide it

## Performance Expectations at 10k/sec

### With Correct Settings:

```
Redis Operations:
├─ Cache GET (pokestop/gym):  1-3ms
├─ Cache HMGET (spawnpoint):  2-5ms  
├─ Stream XADD (queue write): 3-10ms
└─ During BGSAVE (15-30min):  +5-20ms overhead

Result: ✅ All operations complete in <50ms
```

### What Would Indicate a Problem:

```
Symptoms:
├─ Operations taking >100ms consistently
├─ Timeouts at 500ms
├─ Queue sizes growing (XLEN > 10,000)
└─ Redis memory near maxmemory

Actions:
1. Check: docker exec -it golbat-redis redis-cli -a PASSWORD INFO stats
2. Check: docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical
3. Check: pm2 logs golbat-writer (is it processing?)
4. Check: Redis CPU usage (should be <50%)

DO NOT just increase timeout!
```

## The Three Fixes That Matter

### 1. Writer Processing (CRITICAL)

**Before:** Discarded messages without writing
**After:** Actually writes to database in batches

**Impact:** Queue sizes stay small (<1000), Redis stays fast

### 2. BGSAVE Frequency (CRITICAL)

**Before:**
```yaml
--save 60 10000  # Every minute at 10k/sec!
```

**After:**
```yaml
--save 3600 1
--save 1800 100
--save 900 1000  # Every 15-30 minutes
```

**Impact:** Redis not constantly forking/saving, operations stay fast

### 3. Fast Timeouts (ENFORCES SPEED)

**Before:**
```go
ReadTimeout: 3 * time.Second  // Too short for broken Redis
```

**Wrong Fix:**
```go
ReadTimeout: 30 * time.Second  // Masks problems
```

**Correct:**
```go
ReadTimeout: 500 * time.Millisecond  // Enforces speed requirement
```

**Impact:** Fails fast if Redis has issues, forces us to fix root causes

## Why ReadTimeout Matters on Cache Lookups

### The Scenario:

```go
func GetPokestopRecord() {
    // Step 1: L1 cache (in-memory) - instant
    if found in L1 {
        return  // ← 0ms, no Redis needed
    }
    
    // Step 2: L2 cache (Redis) - MUST BE FAST
    redis.Get("pokestop:12345")  // ← Should be 1-5ms!
    
    // Step 3: Database fallback - slow is OK
    db.Query(...)  // ← Can be 50-200ms, rarely hit
}
```

**Redis timeout of 500ms:**
- ✅ Allows 1-5ms normal operation (plenty of headroom)
- ✅ Allows 10-50ms during BGSAVE
- ✅ Catches real Redis issues quickly
- ✅ Falls back to DB if Redis fails

**Redis timeout of 30 seconds:**
- ❌ Hides Redis being completely broken
- ❌ Blocks decode processing for 30 seconds
- ❌ Defeats the purpose of "fast path"
- ❌ No fallback for 30 seconds!

## Monitoring Commands

### Check Redis is FAST:

```bash
# Measure actual latency
docker exec -it golbat-redis redis-cli -a PASSWORD --latency-history

# Should show: min: 0, max: 2, avg: 0.50 (ms)
# During BGSAVE: min: 0, max: 50, avg: 5 (ms)
```

### Check Queue Sizes (Should Be Small):

```bash
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical
# Should be: 0-1000

# If >10,000: Writer isn't keeping up!
```

### Check Writer is Processing:

```bash
pm2 logs golbat-writer --lines 50
# Should see: "Processed batch of X pokestops" every few seconds
```

### Check for Timeouts:

```bash
pm2 logs golbat | grep "context deadline exceeded" | tail -20
# Should be: ZERO!

# If you see timeouts: INVESTIGATE, don't increase timeout!
```

## What If I Still See Timeouts?

### Diagnostic Flow:

1. **Check Redis latency:**
   ```bash
   docker exec -it golbat-redis redis-cli -a PASSWORD --latency
   ```
   If >10ms consistently → Redis issue

2. **Check queue sizes:**
   ```bash
   docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical
   ```
   If >10,000 → Writer not keeping up

3. **Check Redis memory:**
   ```bash
   docker exec -it golbat-redis redis-cli -a PASSWORD INFO memory | grep used_memory
   ```
   If near maxmemory → Eviction happening

4. **Check BGSAVE frequency:**
   ```bash
   docker logs golbat-redis | grep "Background saving" | tail -10
   ```
   If <15 min apart → Config not applied

5. **Check writer is running:**
   ```bash
   pm2 logs golbat-writer
   ```
   Should show "Processed batch..." regularly

### Root Causes and Fixes:

| Symptom | Root Cause | Fix |
|---------|-----------|-----|
| Redis latency >100ms | BGSAVE too frequent | Check docker-compose.redis.yml |
| Queue size >10,000 | Writer not processing | Check golbat-writer binary deployed |
| Memory at maxmemory | Need more Redis RAM | Increase maxmemory in docker-compose |
| Writer not processing | Old binary running | Redeploy golbat-writer |
| Constant BGSAVE | Wrong config active | Restart Redis with correct settings |

**Never just increase the timeout!**

## Summary

✅ **Redis is the fast path** → Operations in milliseconds
✅ **500ms timeout** → Enforces speed requirement  
✅ **Fix root causes** → Writer processing, BGSAVE frequency
✅ **Monitor and investigate** → Don't mask problems with long timeouts

The goal of this refactor was **SPEED**. Keep it that way.

