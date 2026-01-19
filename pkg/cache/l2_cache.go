package cache

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vmihailenco/msgpack/v5"
)

type L2Cache struct {
	client *redis.Client
	ttl    time.Duration
}

func NewL2Cache(client *redis.Client, ttlMinutes int) *L2Cache {
	if ttlMinutes == 0 {
		ttlMinutes = 60
	}

	return &L2Cache{
		client: client,
		ttl:    time.Duration(ttlMinutes) * time.Minute,
	}
}

// Generic get/set for simple types

func (c *L2Cache) Get(ctx context.Context, key string, dest interface{}) error {
	data, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return ErrCacheMiss
	}
	if err != nil {
		return err
	}

	return msgpack.Unmarshal(data, dest)
}

func (c *L2Cache) Set(ctx context.Context, key string, value interface{}) error {
	data, err := msgpack.Marshal(value)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, key, data, c.ttl).Err()
}

func (c *L2Cache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.client.Del(ctx, keys...).Err()
}

// Spawnpoint optimized storage (Redis Hash)

func (c *L2Cache) SetSpawnpoint(ctx context.Context, id int64, lat, lon float64, despawnSec, updated, lastSeen int64) error {
	value := fmt.Sprintf("%.6f,%.6f,%d,%d,%d", lat, lon, despawnSec, updated, lastSeen)
	return c.client.HSet(ctx, "spawnpoints", strconv.FormatInt(id, 10), value).Err()
}

func (c *L2Cache) GetSpawnpoint(ctx context.Context, id int64) (lat, lon float64, despawnSec, updated, lastSeen int64, found bool, err error) {
	value, err := c.client.HGet(ctx, "spawnpoints", strconv.FormatInt(id, 10)).Result()
	if err == redis.Nil {
		return 0, 0, 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, 0, 0, false, err
	}

	parts := strings.Split(value, ",")
	if len(parts) != 5 {
		return 0, 0, 0, 0, 0, false, fmt.Errorf("invalid spawnpoint format")
	}

	lat, _ = strconv.ParseFloat(parts[0], 64)
	lon, _ = strconv.ParseFloat(parts[1], 64)
	despawnSec, _ = strconv.ParseInt(parts[2], 10, 64)
	updated, _ = strconv.ParseInt(parts[3], 10, 64)
	lastSeen, _ = strconv.ParseInt(parts[4], 10, 64)

	return lat, lon, despawnSec, updated, lastSeen, true, nil
}

func (c *L2Cache) BatchGetSpawnpoints(ctx context.Context, ids []int64) (map[int64]SpawnpointData, error) {
	if len(ids) == 0 {
		return make(map[int64]SpawnpointData), nil
	}

	fields := make([]string, len(ids))
	for i, id := range ids {
		fields[i] = strconv.FormatInt(id, 10)
	}

	values, err := c.client.HMGet(ctx, "spawnpoints", fields...).Result()
	if err != nil {
		return nil, err
	}

	result := make(map[int64]SpawnpointData)
	for i, val := range values {
		if val == nil {
			continue
		}

		if str, ok := val.(string); ok {
			parts := strings.Split(str, ",")
			if len(parts) == 5 {
				lat, _ := strconv.ParseFloat(parts[0], 64)
				lon, _ := strconv.ParseFloat(parts[1], 64)
				despawnSec, _ := strconv.ParseInt(parts[2], 10, 64)
				updated, _ := strconv.ParseInt(parts[3], 10, 64)
				lastSeen, _ := strconv.ParseInt(parts[4], 10, 64)

				var despawnSecPtr *int64
				if despawnSec != -1 { // -1 indicates NULL
					despawnSecPtr = &despawnSec
				}

				result[ids[i]] = SpawnpointData{
					Lat:        lat,
					Lon:        lon,
					DespawnSec: despawnSecPtr,
					Updated:    updated,
					LastSeen:   lastSeen,
				}
			}
		}
	}

	return result, nil
}

func (c *L2Cache) BatchSetSpawnpoints(ctx context.Context, spawnpoints map[int64]SpawnpointData) error {
	if len(spawnpoints) == 0 {
		return nil
	}

	pipe := c.client.Pipeline()
	for id, sp := range spawnpoints {
		despawnSec := int64(-1) // -1 indicates NULL
		if sp.DespawnSec != nil {
			despawnSec = *sp.DespawnSec
		}
		value := fmt.Sprintf("%.6f,%.6f,%d,%d,%d", sp.Lat, sp.Lon, despawnSec, sp.Updated, sp.LastSeen)
		pipe.HSet(ctx, "spawnpoints", strconv.FormatInt(id, 10), value)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// Batch operations for other types

func (c *L2Cache) BatchGet(ctx context.Context, keys []string, destMap map[string]interface{}) error {
	if len(keys) == 0 {
		return nil
	}

	pipe := c.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.Get(ctx, key)
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return err
	}

	for i, cmd := range cmds {
		data, err := cmd.Bytes()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			continue
		}

		if dest, ok := destMap[keys[i]]; ok {
			msgpack.Unmarshal(data, dest)
		}
	}

	return nil
}

func (c *L2Cache) BatchSet(ctx context.Context, items map[string]interface{}) error {
	if len(items) == 0 {
		return nil
	}

	pipe := c.client.Pipeline()
	for key, value := range items {
		data, err := msgpack.Marshal(value)
		if err != nil {
			continue
		}
		pipe.Set(ctx, key, data, c.ttl)
	}

	_, err := pipe.Exec(ctx)
	return err
}

type SpawnpointData struct {
	Lat        float64
	Lon        float64
	DespawnSec *int64 // Nullable
	Updated    int64
	LastSeen   int64
}

var ErrCacheMiss = fmt.Errorf("cache miss")

// GetAllKeys returns all keys matching a pattern (for FortTracker initialization)
func (c *L2Cache) GetAllKeys(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	
	for {
		var scanKeys []string
		var err error
		scanKeys, cursor, err = c.client.Scan(ctx, cursor, pattern, 10000).Result()
		if err != nil {
			return nil, err
		}
		
		keys = append(keys, scanKeys...)
		
		if cursor == 0 {
			break
		}
	}
	
	return keys, nil
}

