package worker

import (
	"testing"
	"time"
)

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{"negative attempt", -1, 2 * time.Second},
		{"zero attempt", 0, 2 * time.Second},
		{"first attempt", 1, 2 * time.Second},
		{"second attempt", 2, 4 * time.Second},
		{"before cap", 10, 1024 * time.Second},
		{"last uncapped attempt", 11, 2048 * time.Second},
		{"at cap", 12, time.Hour},
		{"well past cap", 100, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryBackoff(tt.attempts); got != tt.want {
				t.Errorf("retryBackoff(%d) = %v, want %v", tt.attempts, got, tt.want)
			}
		})
	}
}
