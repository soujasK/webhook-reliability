package config

import (
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL   string
	RedisURL      string
	HTTPPort      string
	WorkerCount   int
	MaxAttempts   int
	DispatchEvery int // milliseconds
	RetryEvery    int // milliseconds
	SeedURL       string
}

func Load() Config {
	return Config{
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"),
		// REDIS_URL (a full redis:// connection string, needed for hosted Redis that requires
		// auth, e.g. Railway/Upstash) wins if set; otherwise fall back to plain REDIS_ADDR
		// host:port for local dev, wrapped into a URL redigo can dial.
		RedisURL:      getEnv("REDIS_URL", "redis://"+getEnv("REDIS_ADDR", "localhost:6379")),
		HTTPPort:      getEnv("PORT", "8080"),
		WorkerCount:   getEnvInt("WORKER_COUNT", 4),
		MaxAttempts:   getEnvInt("MAX_ATTEMPTS", 5),
		DispatchEvery: getEnvInt("DISPATCH_INTERVAL_MS", 500),
		RetryEvery:    getEnvInt("RETRY_INTERVAL_MS", 1000),
		// SeedURL, if set, auto-registers a demo endpoint on boot (api/all modes only) so a
		// freshly deployed instance has something to deliver to without a manual seed step.
		SeedURL: getEnv("SEED_URL", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
