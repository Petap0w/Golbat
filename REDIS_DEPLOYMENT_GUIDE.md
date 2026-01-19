# Redis Integration Deployment Guide

## Overview

This refactor introduces Redis-based caching and asynchronous write queuing to handle high-scale operations (10,000+ decodes/second). The system now uses:

- **L1 Cache**: In-memory TTL cache (existing)
- **L2 Cache**: Redis for persistent caching across restarts
- **Write Queue**: Redis Streams for asynchronous database writes
- **Batch Operations**: Optimized batch DB writes via separate writer workers

## Architecture

```
┌─────────────┐
│   Scanner   │
│   Workers   │
└──────┬──────┘
       │ gRPC/HTTP
       ▼
┌─────────────────────────────────────┐
│          Golbat Main Process        │
│  ┌──────────┐      ┌─────────────┐ │
│  │ L1 Cache │◄────►│  L2 (Redis) │ │
│  └──────────┘      └─────────────┘ │
│                           │         │
│                           │ Queue   │
│                           ▼         │
│                    ┌──────────────┐ │
│                    │ Redis Stream │ │
│                    └──────────────┘ │
└─────────────────────────────────────┘
                      │
         ┌────────────┼────────────┐
         │            │            │
         ▼            ▼            ▼
  ┌──────────┐ ┌──────────┐ ┌──────────┐
  │ Writer 1 │ │ Writer 2 │ │ Writer N │
  └─────┬────┘ └─────┬────┘ └─────┬────┘
        │            │            │
        └────────────┼────────────┘
                     ▼
              ┌─────────────┐
              │   Database  │
              └─────────────┘
```

## Performance Improvements

### Expected Results:
- **Startup Time**: Reduced from 15+ minutes to 2-5 minutes (with hot data loading)
- **Database Load**: Reduced by 80-90% (most reads from Redis)
- **Quest Reset**: No more timeouts (writes queued, not blocking)
- **Spawnpoint Lookups**: 100x faster (batch Redis HMGET vs individual DB queries)
- **Write Latency**: 1-5ms to queue (vs 50-200ms direct DB)

### Spawnpoint Optimization:
With your data:
- **Active (7d)**: 39.7M spawnpoints → **Loaded into Redis** (~20GB)
- **Recent (30d)**: 17.6M → DB on-demand
- **Stale (90d)**: 8.9M → DB on-demand
- **Dead (>90d)**: 27M → **Recommend DELETE** (reduces DB load)

## Prerequisites

1. **Redis 7.x** (for streams support)
2. **PM2** (process manager for Go binaries)
3. **100GB+ RAM** for Redis (your server has capacity)
4. **Go 1.21+** for compilation

## Server-Side Setup (You need to do this)

### 1. Install Docker & Docker Compose (if not already installed)

```bash
# Install Docker
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh

# Add your user to docker group
sudo usermod -aG docker $USER
newgrp docker

# Install Docker Compose (if not included)
sudo apt update
sudo apt install docker-compose-plugin

# Verify
docker --version
docker compose version
```

### 2. Start Redis via Docker

```bash
cd /path/to/golbat

# Start Redis in background
docker compose -f docker-compose.redis.yml up -d

# Verify Redis is running
docker ps | grep golbat-redis
docker logs golbat-redis

# Test connection
docker exec -it golbat-redis redis-cli ping
# Should return: PONG

# Optional: Access Redis Commander web UI
# Open browser: http://localhost:8081
```

**Redis Configuration is already set in docker-compose.redis.yml:**
- Max memory: 100GB
- Persistence: AOF enabled
- Data stored in `./redis-data/`

### 3. Install PM2

```bash
# Install Node.js if not present
curl -fsSL https://deb.nodesource.com/setup_18.x | sudo -E bash -
sudo apt-get install -y nodejs

# Install PM2 globally
sudo npm install -g pm2

# Setup PM2 startup script
pm2 startup
# Follow the output instructions
```

### 4. Update Golbat Configuration

Add to your `config.toml`:

```toml
[redis]
enabled = true
addresses = ["localhost:6379"]  # Docker maps to localhost
password = ""
db = 0
pool_size = 100
cache_ttl_minutes = 60
max_queue_size = 1000000
writer_batch_size = 500
writer_workers = 4  # Number of workers in single golbat-writer process
load_hot_on_startup = true
```

### 5. Database Optimization (IMPORTANT!)

#### Delete Dead Spawnpoints (Recommended)
```sql
-- This removes 27M dead spawnpoints, significantly reducing DB load
DELETE FROM spawnpoint 
WHERE last_seen < UNIX_TIMESTAMP() - (90 * 24 * 60 * 60);

-- Optimize table after deletion
OPTIMIZE TABLE spawnpoint;
```

#### Add Indexes (if not present)
```sql
-- Spawnpoint optimization
CREATE INDEX idx_spawnpoint_last_seen ON spawnpoint(last_seen);

-- Pokestop/Gym for batch loading
CREATE INDEX idx_pokestop_id ON pokestop(id);
CREATE INDEX idx_gym_id ON gym(id);
```

#### Partition Spawnpoint Table (Optional, for future scale)
```sql
-- Partition by last_seen for better query performance
ALTER TABLE spawnpoint 
PARTITION BY RANGE (last_seen) (
    PARTITION p_old VALUES LESS THAN (UNIX_TIMESTAMP('2025-01-01')),
    PARTITION p_2025 VALUES LESS THAN (UNIX_TIMESTAMP('2026-01-01')),
    PARTITION p_2026 VALUES LESS THAN (UNIX_TIMESTAMP('2027-01-01')),
    PARTITION p_future VALUES LESS THAN MAXVALUE
);
```

### 6. Build Binaries

```bash
cd /path/to/golbat

# Build main binary
go build -o golbat main.go logsetup.go deviceList.go stats.go routes.go shutdown_unix.go grpc_server_pokemon.go grpc_server_raw.go

# Build writer binary
cd cmd/golbat-writer
go build -o ../../golbat-writer main.go
cd ../..
```

### 7. Deploy with PM2

```bash
# Stop existing golbat if running
pm2 delete all

# Start all processes
pm2 start ecosystem.config.js

# Save PM2 process list
pm2 save

# Monitor
pm2 monit

# View logs
pm2 logs golbat
pm2 logs golbat-writer-1
```

## Deployment Steps

### Step 1: Test on Dev Machine (optional)
```bash
# Start Redis via Docker
docker compose -f docker-compose.redis.yml up -d

# Test compilation
make golbat
cd cmd/golbat-writer && go build -o ../../golbat-writer && cd ../..

# Test run (Ctrl+C to stop)
./golbat
# In another terminal:
./golbat-writer

# Check logs
tail -f logs/golbat-out.log
tail -f logs/golbat-writer-out.log
```

### Step 2: Deploy to Production Server

1. **Backup** current golbat and database
2. **Stop** current golbat instance
3. **Start Redis** via Docker
4. **Deploy** new binaries
5. **Update** config.toml with Redis settings
6. **Delete** dead spawnpoints (optional but recommended)
7. **Start** with PM2

```bash
# On production server
cd /path/to/golbat

# Pull new code from redis branch
git pull origin redis

# Start Redis (if not already running)
docker compose -f docker-compose.redis.yml up -d

# Verify Redis
docker exec -it golbat-redis redis-cli ping

# Build binaries
make golbat
cd cmd/golbat-writer && go build -o ../../golbat-writer && cd ../..

# Update config
nano config.toml
# Add redis section (see config.redis.toml.example)

# Start with PM2
pm2 start ecosystem.config.js
pm2 save
```

### Step 3: Monitor and Verify

```bash
# Check process status
pm2 status

# Watch queue sizes
docker exec -it golbat-redis redis-cli XLEN golbat_writes:critical
docker exec -it golbat-redis redis-cli XLEN golbat_writes:high
docker exec -it golbat-redis redis-cli XLEN golbat_writes:normal

# Monitor Redis memory
docker exec -it golbat-redis redis-cli INFO memory

# Or use Redis Commander web UI
# http://localhost:8081

# Check database load (should be much lower)
# Watch MySQL slow query log
sudo tail -f /var/log/mysql/slow.log
```

## Scaling

### Add More Writer Workers

Edit `config.toml` and increase the number of workers:

```toml
[redis]
writer_workers = 8  # Increase from 4 to 8
```

Then restart the writer:
```bash
pm2 restart golbat-writer
```

**Note**: All workers run in a single `golbat-writer` process as goroutines. This is more efficient than running multiple processes.

### Multiple Golbat Instances (Future)

The architecture supports multiple Golbat main processes reading from the same Redis:

```bash
# On server 1
pm2 start golbat --name golbat-1

# On server 2
pm2 start golbat --name golbat-2

# They share Redis cache and can have separate writers
```

## Troubleshooting

### Issue: "database context deadline exceeded"
**Solution**: Increase writer workers or batch size
```toml
[redis]
writer_workers = 8        # Increase from 4
writer_batch_size = 1000  # Increase from 500
```
Then: `pm2 restart golbat-writer`

### Issue: High Redis memory usage
**Check**:
```bash
docker exec -it golbat-redis redis-cli INFO memory | grep used_memory_human
```
**Solution**: Reduce cache TTL or disable hot loading for less critical data

### Issue: Queue growing indefinitely
**Check**:
```bash
docker exec -it golbat-redis redis-cli XLEN golbat_writes:critical
```
**Solution**: Increase `writer_workers` in config.toml or check DB performance

### Issue: Slow startup
**Expected**: 2-5 minutes with `load_hot_on_startup = true`
**Solution**: Set to `false` for instant startup (on-demand loading)

## Monitoring Commands

```bash
# PM2 monitoring
pm2 monit                    # Interactive monitor
pm2 status                   # Process list
pm2 logs --lines 100         # Recent logs

# Docker/Redis monitoring
docker ps                    # Check Redis container
docker stats golbat-redis    # Resource usage
docker logs golbat-redis     # Redis logs

# Redis CLI (via Docker)
docker exec -it golbat-redis redis-cli INFO stats
docker exec -it golbat-redis redis-cli INFO memory
docker exec -it golbat-redis redis-cli MONITOR  # Debug only

# Queue sizes
docker exec -it golbat-redis redis-cli XLEN golbat_writes:critical
docker exec -it golbat-redis redis-cli XLEN golbat_writes:high
docker exec -it golbat-redis redis-cli XLEN golbat_writes:normal

# Or use Redis Commander web UI: http://localhost:8081

# Database connections
mysql -e "SHOW PROCESSLIST;"
```

## Rollback Plan

If issues arise:

```bash
# Stop new version
pm2 stop all

# Stop Redis
docker compose -f docker-compose.redis.yml down

# Restore old binary
cp golbat.backup golbat

# Disable Redis in config
# redis.enabled = false

# Start old version
pm2 start golbat
```

## Performance Tuning

### For Higher Throughput:
- Increase `writer_workers` to 8-16 (in config.toml)
- Increase `writer_batch_size` to 1000-2000
- Increase Redis `pool_size` to 200

### For Lower Memory:
- Reduce `cache_ttl_minutes` to 30
- Set `load_hot_on_startup = false`
- Delete old spawnpoints aggressively

### For Lower DB Load:
- Ensure hot data is loaded (`load_hot_on_startup = true`)
- Increase writer batch size for fewer transactions
- Add more Redis memory to cache more data

## Next Steps

After successful deployment:

1. **Monitor for 24 hours** to ensure stability
2. **Delete dead spawnpoints** after verifying performance
3. **Scale writer workers** based on queue sizes
4. **Update other decoders** (incident, weather, station, etc.) if needed
5. **Consider partitioning** spawnpoint table for future growth

## Support

If you encounter issues:
1. Check PM2 logs: `pm2 logs`
2. Check Redis logs: `sudo tail -f /var/log/redis/redis-server.log`
3. Check database slow queries
4. Monitor queue sizes in Redis

