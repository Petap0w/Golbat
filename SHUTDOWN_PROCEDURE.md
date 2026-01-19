# Graceful Shutdown Procedure

## The Issue in Your Logs

```
INFO 2026-01-19 18:19:24 Flushing Redis write queue...
WARN 2026-01-19 18:19:24 Failed to get length of golbat_writes:critical: context canceled
WARN 2026-01-19 18:19:24 Failed to get length of golbat_writes:high: context canceled
WARN 2026-01-19 18:19:24 Failed to get length of golbat_writes:normal: context canceled
INFO 2026-01-19 18:19:24 All queues flushed successfully  ← FALSE!
```

**What happened:**
- The main context was canceled during shutdown
- Flush tried to check queue sizes with canceled context → failed
- Reported "success" even though it couldn't verify queues were empty
- **Pending writes may have been lost!**

## The Fix

Updated `pkg/queue/write_queue.go`:
- Flush now uses `context.Background()` with its own 30-second timeout
- Won't fail immediately when parent context is canceled
- Actually waits for queues to drain
- Reports true status of remaining items

## Architecture: Two Separate Processes

```
┌─────────────┐         Redis Streams          ┌──────────────────┐
│   Golbat    │────────────────────────────────▶│ golbat-writer    │
│  (decoder)  │  Writes to queue               │ (DB writer)      │
└─────────────┘                                 └──────────────────┘
      │                                                   │
      │ Shutdown: Flushes queue                          │ Shutdown: Stops processing
      ▼                                                   ▼
```

**Important:** These are **separate PM2 processes**!

## Proper Shutdown Sequence

### Option 1: Graceful Restart (Recommended)

```bash
# Step 1: Stop Golbat (stops new data coming in)
pm2 stop golbat

# Step 2: Wait for writer to drain queue (check queue size)
watch -n 1 'docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical'
# Wait until it shows: (integer) 0

# Step 3: Now safe to restart writer
pm2 restart golbat-writer

# Step 4: Restart Golbat
pm2 start golbat
```

### Option 2: Quick Restart (Some Data Loss Acceptable)

```bash
# Restart both at once (small window of potential data loss)
pm2 restart all
```

### Option 3: Emergency Stop

```bash
# Force stop everything
pm2 stop all

# Check for pending writes
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:high  
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:normal

# If queues have items, manually start writer to drain them
pm2 start golbat-writer
# Wait for queues to drain...
pm2 stop golbat-writer

# Now restart everything
pm2 start all
```

## What Happens During Shutdown

### Golbat Shutdown:
1. ✅ Stops accepting new gRPC/HTTP requests
2. ✅ Cancels main context
3. ✅ Waits for active goroutines to finish
4. ✅ Flushes remaining writes to Redis queue (30-second timeout)
5. ✅ Closes Redis connection
6. ✅ Exits

### golbat-writer Shutdown:
1. ✅ Stops reading from Redis Streams
2. ✅ Finishes processing current batch
3. ✅ Exits

## The Problem with Simultaneous Shutdown

**If both shut down at the same time:**
```
T=0:   pm2 restart all
       ├─ Golbat: Stop accepting requests, flush queue
       └─ Writer: Stop reading queue, finish batch

T=1s:  Golbat: "Flushing queue, 1000 items remaining..."
       Writer: Already stopped! Not processing anymore!

T=30s: Golbat: "Timeout, 1000 items still pending"
       ❌ These 1000 items will be lost!
```

## Monitoring Queue Health

### Check Queue Sizes:
```bash
# Critical priority (pokestops, gyms, spawnpoints)
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical

# High priority (incidents, tappables, weather)
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:high

# Normal priority (routes, s2cells, players)
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:normal
```

**Healthy:** 0-1000 items  
**Warning:** 1000-10,000 items (writer may be slow)  
**Critical:** >10,000 items (writer can't keep up!)

### Check Writer is Processing:
```bash
pm2 logs golbat-writer --lines 20

# Should see:
# INFO Processed batch of X pokestops
# INFO Processed batch of X gyms
# (every few seconds)
```

## Best Practices

### For Config Changes (No Data Loss):
```bash
# Edit config.toml
pm2 stop golbat          # Stop new data
# Wait 10-30 seconds for queue to drain
pm2 restart golbat       # Apply new config
```

### For Binary Updates (Minimal Data Loss):
```bash
# Build new binaries
make golbat
go build -o golbat-writer ./cmd/golbat-writer

# Deploy
pm2 stop golbat
# Wait for queue to drain
pm2 restart golbat-writer  # Deploy new writer
pm2 start golbat            # Deploy new golbat
```

### For Redis Maintenance (Coordinate Carefully):
```bash
# Stop all writes first!
pm2 stop golbat

# Wait for queue to drain completely
docker exec -it golbat-redis redis-cli -a PASSWORD XLEN golbat_writes:critical
# Should be: (integer) 0

# Stop writer
pm2 stop golbat-writer

# Now safe to restart Redis
docker compose -f docker-compose.redis.yml restart

# Restart services
pm2 start all
```

## Expected Behavior After Fix

**Good shutdown:**
```
INFO Starting shutdown...
INFO Flushing Redis write queue...
INFO Waiting for queues to flush: 234 items remaining
INFO Waiting for queues to flush: 89 items remaining
INFO Waiting for queues to flush: 12 items remaining
INFO All queues flushed successfully  ✅
INFO Closing Redis connection...
INFO Golbat exiting!
```

**Timeout (writer stopped):**
```
INFO Starting shutdown...
INFO Flushing Redis write queue...
INFO Waiting for queues to flush: 234 items remaining
INFO Waiting for queues to flush: 234 items remaining  ← Not decreasing!
INFO Waiting for queues to flush: 234 items remaining
WARN Queue flush timeout, 234 writes may be pending  ⚠️
INFO Golbat exiting!
```

## Summary

✅ **Fixed:** Flush now uses independent context, actually waits for queue  
⚠️ **Limitation:** Golbat and writer are separate processes - coordinate shutdowns!  
📋 **Best Practice:** Stop Golbat → Wait for drain → Restart both

The fix ensures Golbat **tries** to flush gracefully. But if writer is already stopped, pending writes will remain in Redis Streams until writer restarts (they're not lost, just delayed).

