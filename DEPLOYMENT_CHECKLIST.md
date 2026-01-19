# Redis Deployment Checklist

## ⚠️ CRITICAL BUG FIXED

**READ `CRITICAL_BUG_FIX.md` FIRST!**

The writer was **NOT actually writing to the database** - it was just ACKing messages and discarding them. This has been fixed. You MUST deploy the new `golbat-writer` binary for the system to work.

---

# Deployment Steps

## Before Deployment

- [ ] Read `REDIS_SETTINGS_EXPLAINED.md` to understand the configuration
- [ ] Confirm your scale (decodes/sec, data volumes)
- [ ] Decide: Same VM or separate Redis VM?

## On Redis VM

### 1. Generate Password
```bash
openssl rand -base64 32
# Save this password!
```

### 2. Update docker-compose.redis.yml
```bash
# Replace YOUR_REDIS_PASSWORD_HERE in TWO places:
# - Line 13: --requirepass YOUR_PASSWORD
# - Line 40: environment variable for redis-commander
```

### 3. Start Redis
```bash
docker compose -f docker-compose.redis.yml up -d
```

### 4. Verify Redis is Running
```bash
# Check logs
docker logs golbat-redis --tail 50

# Test connection
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD ping
# Should return: PONG

# Check settings
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD CONFIG GET save
# Should show: 3600 1 1800 100 900 1000

docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD CONFIG GET maxmemory-policy
# Should show: allkeys-lru
```

### 5. Open Firewall (If Separate VM)
```bash
# Allow Golbat VM to connect
sudo ufw allow from GOLBAT_VM_IP to any port 6379
sudo ufw status
```

## On Golbat VM

### 1. Update config.toml
```toml
[redis]
enabled = true
addresses = ["REDIS_VM_IP:6379"]  # or ["127.0.0.1:6379"] if same VM
password = "YOUR_REDIS_PASSWORD"
db = 0
pool_size = 200
cache_ttl_minutes = 120
max_queue_size = 100000
writer_batch_size = 1000
writer_workers = 8
load_hot_on_startup = true
```

### 2. Test Connection from Golbat VM
```bash
# Install redis-cli if needed
apt-get install redis-tools

# Test connection
redis-cli -h REDIS_VM_IP -a YOUR_PASSWORD ping
# Should return: PONG
```

### 3. Stop Old Golbat
```bash
pm2 stop golbat
pm2 stop golbat-writer  # if exists
```

### 4. Deploy New Binaries
```bash
# Copy new golbat and golbat-writer binaries
# Make sure config.toml is updated

# Start services
pm2 start ecosystem.config.js

# Watch startup
pm2 logs golbat --lines 100
```

### 5. Verify Startup
```bash
# Check for these log lines:
# ✅ "Connected to Redis at X:6379"
# ✅ "Loading hot spawnpoints into Redis..."
# ✅ "Loaded X pokestops to Redis"
# ✅ "Loaded X gyms to Redis"
# ✅ "Golbat started"

# Check PM2 status
pm2 status

# Monitor for errors
pm2 logs --lines 50
```

## Verification Checks

### Check Redis Memory Usage
```bash
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD INFO memory | grep -E "used_memory_human|maxmemory"
```

### Check Queue Sizes
```bash
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD XLEN golbat_writes:critical
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD XLEN golbat_writes:high
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD XLEN golbat_writes:normal
```

Expected: 0-1000 during normal operation

### Check Writer is Processing
```bash
pm2 logs golbat-writer --lines 50
# Should see: "Processed batch of X pokestops"
```

### Monitor for Timeouts
```bash
pm2 logs golbat | grep "context deadline exceeded"
# Should be ZERO after fixes!
```

## Troubleshooting

### Still Getting Timeouts?

1. **Check Redis isn't doing constant BGSAVE:**
```bash
docker logs golbat-redis | grep "Background saving" | tail -20
```
Should be every 15-30 minutes, NOT every minute!

2. **Check Redis memory isn't full:**
```bash
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD INFO memory
```
If `used_memory` ≈ `maxmemory`, increase maxmemory in docker-compose.

3. **Check queue isn't growing:**
```bash
docker exec -it golbat-redis redis-cli -a YOUR_PASSWORD XLEN golbat_writes:critical
```
If > 10,000, writer isn't keeping up. Increase `writer_workers` in config.

4. **Check network latency (if separate VM):**
```bash
# From Golbat VM
ping REDIS_VM_IP -c 10
```
Should be < 1ms on same network.

### Redis Commander Access

Visit: `http://REDIS_VM_IP:8081`

- Browse keys
- Monitor memory
- View queue sizes
- Execute commands

## Settings Files Modified

- `pkg/redis/client.go` → Timeouts increased to 30s
- `docker-compose.redis.yml` → Save policy relaxed, maxmemory-policy changed
- `config.toml` → Redis connection settings

## Performance Expectations

**After Deployment:**
- ✅ **Redis operations: 1-5ms** (this is the whole point!)
- ✅ No "context deadline exceeded" errors
- ✅ Startup in 30-60 seconds (hot load)
- ✅ Queue sizes stay < 1000
- ✅ Redis BGSAVE every 15-30 minutes
- ✅ 10k/sec decode rate sustained
- ✅ Memory usage stable at ~30-35GB Redis, ~35-45GB Golbat

**Monitor actual Redis latency:**
```bash
docker exec -it golbat-redis redis-cli -a PASSWORD --latency-history
# Should show avg: 0.5-5ms, max: <50ms
```

**If operations are slow (>100ms) or timing out:**
→ **DO NOT increase timeout!**
→ Read `SPEED_FIRST_APPROACH.md` for diagnostic steps
→ Fix the root cause

