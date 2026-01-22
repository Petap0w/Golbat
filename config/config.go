package config

import (
	"time"

	"golbat/geo"
)

type configDefinition struct {
	Port                           int        `koanf:"port"`
	GrpcPort                       int        `koanf:"grpc_port"`
	Webhooks                       []Webhook  `koanf:"webhooks"`
	Database                       database   `koanf:"database"`
	Redis                          redis      `koanf:"redis"`
	Logging                        logging    `koanf:"logging"`
	Sentry                         sentry     `koanf:"sentry"`
	Pyroscope                      pyroscope  `koanf:"pyroscope"`
	Prometheus                     Prometheus `koanf:"prometheus"`
	PokemonMemoryOnly              bool       `koanf:"pokemon_memory_only"`
	PokemonInternalToDb            bool       `koanf:"pokemon_internal_to_db"`
	TestFortInMemory               bool       `koanf:"test_fort_in_memory"`
	Cleanup                        cleanup    `koanf:"cleanup"`
	RawBearer                      string     `koanf:"raw_bearer"`
	ApiSecret                      string     `koanf:"api_secret"`
	Pvp                            pvp        `koanf:"pvp"`
	Koji                           koji       `koanf:"koji"`
	Tuning                         tuning     `koanf:"tuning"`
	Weather                        weather    `koanf:"weather"`
	ScanRules                      []scanRule `koanf:"scan_rules"`
	MaxConcurrentProactiveIVSwitch int        `koanf:"max_concurrent_proactive_iv_switch"`
}

func (configDefinition configDefinition) GetWebhookInterval() time.Duration {
	// not currently configurable.
	return time.Second
}

func (configDefinition configDefinition) GetWebhooks() []Webhook {
	return configDefinition.Webhooks
}

func (configDefinition configDefinition) GetPrometheus() Prometheus {
	return configDefinition.Prometheus
}

type koji struct {
	Url         string `koanf:"url"`
	BearerToken string `koanf:"bearer_token"`
}

type cleanup struct {
	Pokemon             bool  `koanf:"pokemon"`
	Quests              bool  `koanf:"quests"`
	Incidents           bool  `koanf:"incidents"`
	Tappables           bool  `koanf:"tappables"`
	Stats               bool  `koanf:"stats"`
	StatsDays           int   `koanf:"stats_days"`
	DeviceHours         int   `koanf:"device_hours"`
	FortsStaleThreshold int64 `koanf:"forts_stale_threshold"` // seconds, default 3600 (1 hour)
}

type Webhook struct {
	Url       string            `koanf:"url"`
	Types     []string          `koanf:"types"`
	Areas     []string          `koanf:"areas"`
	Headers   []string          `koanf:"headers"`
	HeaderMap map[string]string `koanf:"-"`
	AreaNames []geo.AreaName    `koanf:"-"`
}

type pvp struct {
	Enabled               bool   `koanf:"enabled"`
	IncludeHundosUnderCap bool   `koanf:"include_hundos_under_cap"`
	LevelCaps             []int  `koanf:"level_caps"`
	RankingComparator     string `koanf:"ranking_comparator"`
}

type sentry struct {
	DSN              string  `koanf:"dsn"`
	SampleRate       float64 `koanf:"sample_rate"`
	EnableTracing    bool    `koanf:"enable_tracing"`
	TracesSampleRate float64 `koanf:"traces_sample_rate"`
}

type pyroscope struct {
	ApplicationName      string `koanf:"application_name"`
	ServerAddress        string `koanf:"server_address"`
	BasicAuthUser        string `koanf:"basic_auth_user"`
	BasicAuthPassword    string `koanf:"basic_auth_password"`
	Logger               bool   `koanf:"logger"`
	MutexProfileFraction int    `koanf:"mutex_profile_fraction"`
	BlockProfileRate     int    `koanf:"block_profile_rate"`

	// Deprecated
	ApiKey string `koanf:"api_key"`
}

type Prometheus struct {
	Enabled        bool      `koanf:"enabled"`
	Token          string    `koanf:"token"`
	BucketSize     []float64 `koanf:"bucket_size"`
	LiveStats      bool      `koanf:"live_stats"`
	LiveStatsSleep int       `koanf:"live_stats_sleep"`
}

type logging struct {
	Debug      bool `koanf:"debug"`
	SaveLogs   bool `koanf:"save_logs"`
	MaxSize    int  `koanf:"max_size"`
	MaxBackups int  `koanf:"max_backups"`
	MaxAge     int  `koanf:"max_age"`
	Compress   bool `koanf:"compress"`
}

type database struct {
	Addr     string `koanf:"address"`
	User     string `koanf:"user"`
	Password string `koanf:"password"`
	Db       string `koanf:"db"`
	MaxPool  int    `koanf:"max_pool"`
}

type redis struct {
	Enabled                 bool                  `koanf:"enabled"`
	Addresses               []string              `koanf:"addresses"`
	Password                string                `koanf:"password"`
	DB                      int                   `koanf:"db"`
	PoolSize                int                   `koanf:"pool_size"`
	StreamTrimTarget        int64                 `koanf:"stream_trim_target"` // Target size to trim streams to (e.g., 100000)
	WriterBatchSize         int                   `koanf:"writer_batch_size"`
	WriterWorkers           int                   `koanf:"writer_workers"`
	PipelineBatchSize       int                   `koanf:"pipeline_batch_size"`        // Flush pipeline every N writes (default: 500)
	PipelineFlushMs         int                   `koanf:"pipeline_flush_ms"`          // Flush pipeline every N milliseconds (default: 25)
	PersistentCacheEnabled  bool                  `koanf:"persistent_cache_enabled"`   // Enable 24h Redis persistent cache + startup loading for all static objects
	PersistentCacheTTLHours int                   `koanf:"persistent_cache_ttl_hours"` // How long to keep data in Redis persistent cache (default: 24)
	PersistentCacheConfig   persistentCacheConfig `koanf:"persistent_cache"`           // Persistent cache age filters and trimming
}

// persistentCacheConfig defines age filters and trimming for persistent cache
type persistentCacheConfig struct {
	// Max age (in days) for loading objects on startup - only load recent data
	PokestopMaxAgeDays   int `koanf:"pokestop_max_age_days"`   // Default: 30 days
	GymMaxAgeDays        int `koanf:"gym_max_age_days"`        // Default: 30 days
	StationMaxAgeDays    int `koanf:"station_max_age_days"`    // Default: 90 days
	RouteMaxAgeDays      int `koanf:"route_max_age_days"`      // Default: 90 days
	SpawnpointMaxAgeDays int `koanf:"spawnpoint_max_age_days"` // Default: 7 days (was hardcoded)

	// Automatic trimming of stale data from Redis
	TrimEnabled       bool `koanf:"trim_enabled"`        // Enable periodic trimming (default: true)
	TrimIntervalHours int  `koanf:"trim_interval_hours"` // How often to trim (default: 24 hours)
}

// GetMaxAgeDays returns the max age for a given object type with defaults
func (p *persistentCacheConfig) GetMaxAgeDays(objectType string) int {
	switch objectType {
	case "pokestop":
		if p.PokestopMaxAgeDays > 0 {
			return p.PokestopMaxAgeDays
		}
		return 30 // Default: 30 days
	case "gym":
		if p.GymMaxAgeDays > 0 {
			return p.GymMaxAgeDays
		}
		return 30 // Default: 30 days
	case "station":
		if p.StationMaxAgeDays > 0 {
			return p.StationMaxAgeDays
		}
		return 90 // Default: 90 days
	case "route":
		if p.RouteMaxAgeDays > 0 {
			return p.RouteMaxAgeDays
		}
		return 90 // Default: 90 days
	case "spawnpoint":
		if p.SpawnpointMaxAgeDays > 0 {
			return p.SpawnpointMaxAgeDays
		}
		return 7 // Default: 7 days (hot spawnpoints)
	default:
		return 30 // Generic default
	}
}

type tuning struct {
	ExtendedTimeout    bool    `koanf:"extended_timeout"`
	MaxPokemonResults  int     `koanf:"max_pokemon_results"`
	MaxPokemonDistance float64 `koanf:"max_pokemon_distance"`
	ProfileRoutes      bool    `koanf:"profile_routes"`
	StartupDelaySec    int     `koanf:"startup_delay_sec"` // Delay after loading before GRPC starts (default: 0)

	// Force update intervals (seconds) - even if object hasn't changed
	// Objects are re-saved after this interval to confirm they still exist
	ForceUpdatePokestop   int64 `koanf:"force_update_pokestop"`   // Default: 900 (15 min)
	ForceUpdateGym        int64 `koanf:"force_update_gym"`        // Default: 900 (15 min)
	ForceUpdateStation    int64 `koanf:"force_update_station"`    // Default: 3600 (1 hour)
	ForceUpdateRoute      int64 `koanf:"force_update_route"`      // Default: 86400 (24 hours)
	ForceUpdateSpawnpoint int64 `koanf:"force_update_spawnpoint"` // Default: 86400 (24 hours)
}

type scanRule struct {
	Areas                    []string       `koanf:"areas"`
	AreaNames                []geo.AreaName `koanf:"-"`
	ScanContext              []string       `koanf:"context"`
	ProcessPokemon           *bool          `koanf:"pokemon"`
	ProcessWilds             *bool          `koanf:"wild_pokemon"`
	ProcessNearby            *bool          `koanf:"nearby_pokemon"`
	ProcessWeather           *bool          `koanf:"weather"`
	ProcessCells             *bool          `koanf:"cells"`
	ProcessPokestops         *bool          `koanf:"pokestops"`
	ProcessGyms              *bool          `koanf:"gyms"`
	ProcessStations          *bool          `koanf:"stations"`
	ProcessTappables         *bool          `koanf:"tappables"`
	ProactiveIVSwitching     *bool          `koanf:"proactive_iv_switching"`
	ProactiveIVSwitchingToDB *bool          `koanf:"proactive_iv_switching_to_db"`
}

type weather struct {
	ProactiveIVSwitching     bool `koanf:"proactive_iv_switching"`
	ProactiveIVSwitchingToDB bool `koanf:"proactive_iv_switching_to_db"`
}

// GetForceUpdateInterval returns the force update interval for a data type with sensible defaults
func (t tuning) GetForceUpdateInterval(dataType string) int64 {
	switch dataType {
	case "pokestop":
		if t.ForceUpdatePokestop > 0 {
			return t.ForceUpdatePokestop
		}
		return 900 // 15 minutes - quests/lures change frequently
	case "gym":
		if t.ForceUpdateGym > 0 {
			return t.ForceUpdateGym
		}
		return 900 // 15 minutes - raids change frequently
	case "station":
		if t.ForceUpdateStation > 0 {
			return t.ForceUpdateStation
		}
		return 3600 // 1 hour - battles don't change often
	case "route":
		if t.ForceUpdateRoute > 0 {
			return t.ForceUpdateRoute
		}
		return 86400 // 24 hours - routes are very static
	case "spawnpoint":
		if t.ForceUpdateSpawnpoint > 0 {
			return t.ForceUpdateSpawnpoint
		}
		return 86400 // 24 hours - spawn times are very static
	default:
		return 900 // Default: 15 minutes
	}
}

var Config configDefinition
