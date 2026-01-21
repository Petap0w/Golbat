package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"

	"golbat/config"
	golbatRedis "golbat/pkg/redis"
	"golbat/pkg/writer"
)

func main() {
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Load config
	cfg, err := config.ReadConfig()
	if err != nil {
		log.Fatalf("Failed to read config: %s", err)
	}

	// Setup logging
	logLevel := log.InfoLevel
	if cfg.Logging.Debug {
		logLevel = log.DebugLevel
	}
	log.SetLevel(logLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	log.Info("Golbat Writer starting...")

	// Check Redis is enabled
	if !cfg.Redis.Enabled {
		log.Fatal("Redis must be enabled to run golbat-writer")
	}

	// Connect to Redis
	redisClient, err := golbatRedis.NewClient(&golbatRedis.Config{
		Enabled:   cfg.Redis.Enabled,
		Addresses: cfg.Redis.Addresses,
		Password:  cfg.Redis.Password,
		DB:        cfg.Redis.DB,
		PoolSize:  cfg.Redis.PoolSize,
	})
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %s", err)
	}
	defer redisClient.Close()

	log.Info("Connected to Redis")

	// Connect to database
	mysqlConfig := mysql.Config{
		User:                 cfg.Database.User,
		Passwd:               cfg.Database.Password,
		Net:                  "tcp",
		Addr:                 cfg.Database.Addr,
		DBName:               cfg.Database.Db,
		AllowNativePasswords: true,
	}

	db, err := sqlx.Open("mysql", mysqlConfig.FormatDSN())
	if err != nil {
		log.Fatalf("Failed to connect to database: %s", err)
	}
	defer db.Close()

	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(cfg.Database.MaxPool)
	db.SetMaxIdleConns(10)
	db.SetConnMaxIdleTime(time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %s", err)
	}

	log.Info("Connected to database")

	// Determine number of workers
	numWorkers := cfg.Redis.WriterWorkers
	if numWorkers == 0 {
		numWorkers = 4 // Default to 4 workers
	}

	batchSize := cfg.Redis.WriterBatchSize
	if batchSize == 0 {
		batchSize = 500
	}

	trimTarget := cfg.Redis.StreamTrimTarget
	if trimTarget == 0 {
		trimTarget = 100000 // Default: keep 100k messages
	}

	log.Infof("Starting %d DB writer workers (batch size: %d, trim target: %d)", numWorkers, batchSize, trimTarget)

	// Start multiple workers
	workerDone := make(chan error, numWorkers)
	for i := 0; i < numWorkers; i++ {
		workerID := fmt.Sprintf("writer-%d", i+1)
		dbWriter := writer.NewDBWriter(redisClient.GetClient(), db, workerID, batchSize, trimTarget)
		
		go func(id string, w *writer.DBWriter) {
			log.Infof("Worker %s started", id)
			err := w.Run(ctx)
			if err != nil {
				log.Errorf("Worker %s error: %s", id, err)
			}
			workerDone <- err
		}(workerID, dbWriter)
	}

	log.Infof("All %d workers running", numWorkers)

	// Wait for shutdown signal
	select {
	case sig := <-sigChan:
		log.Infof("Received signal %s, shutting down gracefully...", sig)
		cancelFn()

		// Wait for all workers to finish with timeout
		shutdownTimeout := time.After(30 * time.Second)
		workersFinished := 0
		
		for workersFinished < numWorkers {
			select {
			case err := <-workerDone:
				workersFinished++
				if err != nil {
					log.Warnf("Worker finished with error: %s", err)
				}
				log.Infof("Workers finished: %d/%d", workersFinished, numWorkers)
			case <-shutdownTimeout:
				log.Warnf("Shutdown timeout, %d/%d workers finished", workersFinished, numWorkers)
				goto exit
			}
		}
		log.Info("All workers finished gracefully")

	case err := <-workerDone:
		if err != nil {
			log.Errorf("Worker stopped unexpectedly: %s", err)
		}
		cancelFn()
	}

exit:
	log.Info("Golbat Writer stopped")
}

