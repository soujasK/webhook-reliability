package config

import "testing"

// clearEnv wipes every var Load() reads so each test starts from a known state, regardless of
// what's set in the shell running `go test`.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DATABASE_URL", "REDIS_URL", "REDIS_ADDR", "PORT",
		"WORKER_COUNT", "MAX_ATTEMPTS", "DISPATCH_INTERVAL_MS", "RETRY_INTERVAL_MS", "SEED_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg := Load()

	if cfg.RedisURL != "redis://localhost:6379" {
		t.Errorf("default RedisURL = %q, want %q", cfg.RedisURL, "redis://localhost:6379")
	}
	if cfg.HTTPPort != "8080" {
		t.Errorf("default HTTPPort = %q, want %q", cfg.HTTPPort, "8080")
	}
	if cfg.WorkerCount != 4 {
		t.Errorf("default WorkerCount = %d, want 4", cfg.WorkerCount)
	}
	if cfg.MaxAttempts != 5 {
		t.Errorf("default MaxAttempts = %d, want 5", cfg.MaxAttempts)
	}
	if cfg.SeedURL != "" {
		t.Errorf("default SeedURL = %q, want empty (auto-seed off by default)", cfg.SeedURL)
	}
}

func TestLoadRedisAddrFallback(t *testing.T) {
	// Local dev / docker-compose set REDIS_ADDR (host:port), not REDIS_URL. Load() must still
	// hand the queue package something redis.DialURL can dial.
	clearEnv(t)
	t.Setenv("REDIS_ADDR", "redis:6379")

	cfg := Load()

	if cfg.RedisURL != "redis://redis:6379" {
		t.Errorf("RedisURL from REDIS_ADDR = %q, want %q", cfg.RedisURL, "redis://redis:6379")
	}
}

func TestLoadRedisURLWins(t *testing.T) {
	// Hosted Redis (Railway, Upstash) gives a full auth-bearing URL via REDIS_URL. That must take
	// priority over REDIS_ADDR even if both happen to be set.
	clearEnv(t)
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_URL", "redis://default:secret@hosted-redis:6379")

	cfg := Load()

	if cfg.RedisURL != "redis://default:secret@hosted-redis:6379" {
		t.Errorf("RedisURL = %q, want the explicit REDIS_URL value", cfg.RedisURL)
	}
}

func TestLoadIntOverrideAndInvalidFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("MAX_ATTEMPTS", "9")
	t.Setenv("WORKER_COUNT", "not-a-number") // malformed input must fall back, not crash Load()

	cfg := Load()

	if cfg.MaxAttempts != 9 {
		t.Errorf("MaxAttempts = %d, want 9", cfg.MaxAttempts)
	}
	if cfg.WorkerCount != 4 {
		t.Errorf("WorkerCount with malformed env = %d, want fallback 4", cfg.WorkerCount)
	}
}

func TestLoadSeedURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("SEED_URL", "http://mock-receiver.railway.internal:9090/receive")

	cfg := Load()

	if cfg.SeedURL != "http://mock-receiver.railway.internal:9090/receive" {
		t.Errorf("SeedURL = %q, want the configured value", cfg.SeedURL)
	}
}
