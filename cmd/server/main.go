package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"webhook-engine/internal/api"
	"webhook-engine/internal/config"
	"webhook-engine/internal/queue"
	"webhook-engine/internal/store"
	"webhook-engine/internal/worker"
)

func main() {
	mode := flag.String("mode", "all", "one of: api, worker, dispatcher, scheduler, seed, all")
	seedURL := flag.String("seed-url", "http://localhost:9090/receive", "endpoint URL to insert when --mode=seed")
	flag.Parse()

	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer db.Close()

	if err := store.RunMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	redisClient := queue.NewRedisClient(cfg.RedisAddr)

	switch *mode {
	case "seed":
		id, err := store.SeedDemoEndpoint(ctx, db, *seedURL)
		if err != nil {
			log.Fatalf("seed failed: %v", err)
		}
		log.Printf("seeded demo endpoint %s -> %s (subscribed to payment.succeeded)", id, *seedURL)

	case "api":
		runAPI(ctx, db, redisClient, cfg.HTTPPort)

	case "worker":
		log.Printf("starting %d worker goroutines", cfg.WorkerCount)
		worker.NewPool(db, redisClient, cfg.MaxAttempts).Run(ctx, cfg.WorkerCount)

	case "dispatcher":
		log.Println("starting dispatcher")
		queue.RunDispatcher(ctx, db, redisClient, cfg.DispatchEvery)

	case "scheduler":
		log.Println("starting retry scheduler")
		queue.RunRetryScheduler(ctx, db, redisClient, cfg.RetryEvery)

	case "all":
		log.Println("starting api + dispatcher + scheduler + workers in one process (local dev mode)")
		go queue.RunDispatcher(ctx, db, redisClient, cfg.DispatchEvery)
		go queue.RunRetryScheduler(ctx, db, redisClient, cfg.RetryEvery)
		go worker.NewPool(db, redisClient, cfg.MaxAttempts).Run(ctx, cfg.WorkerCount)
		runAPI(ctx, db, redisClient, cfg.HTTPPort)

	default:
		log.Fatalf("unknown --mode %q", *mode)
	}
}

func runAPI(ctx context.Context, db *sql.DB, redisClient *queue.RedisClient, port string) {
	server := api.NewServer(db, redisClient)
	httpServer := &http.Server{Addr: ":" + port, Handler: server.Routes()}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("api: graceful shutdown error: %v", err)
		}
	}()

	log.Printf("api listening on :%s", port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("api: %v", err)
	}
}
