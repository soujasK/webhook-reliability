package config

import "testing"

func TestGetEnvPositiveInt(t *testing.T) {
	const key = "WEBHOOK_TEST_POSITIVE_INT"
	for _, tc := range []struct {
		name, value string
		want        int
	}{
		{"valid", "12", 12},
		{"zero", "0", 4},
		{"negative", "-8", 4},
		{"invalid", "abc", 4},
		{"overflow", "9999999999999999999999999999999", 4},
		{"empty", "", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.value)
			if got := getEnvPositiveInt(key, 4); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLoadRejectsNonPositiveSettings(t *testing.T) {
	for _, key := range []string{"WORKER_COUNT", "MAX_ATTEMPTS", "DISPATCH_INTERVAL_MS", "RETRY_INTERVAL_MS"} {
		t.Setenv(key, "-1")
	}
	cfg := Load()
	if cfg.WorkerCount != 4 || cfg.MaxAttempts != 5 || cfg.DispatchEvery != 500 || cfg.RetryEvery != 1000 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}
