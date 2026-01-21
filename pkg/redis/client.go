package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

type Config struct {
	Enabled   bool     `koanf:"enabled"`
	Addresses []string `koanf:"addresses"`
	Password  string   `koanf:"password"`
	DB        int      `koanf:"db"`
	PoolSize  int      `koanf:"pool_size"`
}

type Client struct {
	client *redis.Client
	config *Config
}

func NewClient(cfg *Config) (*Client, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("redis not enabled")
	}

	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("no redis addresses configured")
	}

	poolSize := cfg.PoolSize
	if poolSize == 0 {
		poolSize = 50 // Default pool size
	}

	opts := &redis.Options{
		Addr:         cfg.Addresses[0],
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     poolSize,
		MinIdleConns: 10,
		MaxRetries:   3,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  500 * time.Millisecond, // Fast cache reads (1-5ms normal, <50ms during BGSAVE)
		WriteTimeout: 20 * time.Second,       // Increased to tolerate XAUTOCLAIM blocking (10-15s)
		PoolTimeout:  3 * time.Second,        // Connection acquisition timeout
	}

	client := redis.NewClient(opts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	log.Infof("Connected to Redis at %s (pool size: %d)", cfg.Addresses[0], poolSize)

	return &Client{
		client: client,
		config: cfg,
	}, nil
}

func (c *Client) GetClient() *redis.Client {
	return c.client
}

func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *Client) IsEnabled() bool {
	return c.config.Enabled && c.client != nil
}

// PoolStats returns connection pool statistics
func (c *Client) PoolStats() *redis.PoolStats {
	if c.client == nil {
		return nil
	}
	return c.client.PoolStats()
}
