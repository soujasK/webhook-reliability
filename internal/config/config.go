package config

import (
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL   string
	RedisAddr     string
	HTTPPort      string
	WorkerCount   int
	MaxAttempts   int
	DispatchEvery int // milliseconds
	RetryEvery    int // milliseconds
}

func Load() Config {
	return Config{
		DatabaseURL:   getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"),
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		HTTPPort:      getEnv("PORT", "8080"),
		WorkerCount:   getEnvPositiveInt("WORKER_COUNT", 4),
		MaxAttempts:   getEnvPositiveInt("MAX_ATTEMPTS", 5),
		DispatchEvery: getEnvPositiveInt("DISPATCH_INTERVAL_MS", 500),
		RetryEvery:    getEnvPositiveInt("RETRY_INTERVAL_MS", 1000),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvPositiveInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
