package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// testDB connects to a real Postgres and runs migrations against it. These are integration
// tests, not unit tests: they need `docker compose up postgres` (or an equivalent instance)
// reachable at DATABASE_URL. If it isn't, the test skips rather than failing the whole suite, so
// `go test ./...` still passes on a machine with no DB running.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("skipping integration test: cannot open postgres connection: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("skipping integration test: postgres not reachable at %s (start it with `docker compose up postgres` and re-run): %v", dsn, err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mustCreateEndpoint inserts a throwaway endpoint + subscription for a test to fan out
// deliveries against. Each test uses a unique event_type/URL suffix so repeated runs against a
// shared dev database don't collide with each other or with prior runs.
func mustCreateEndpoint(t *testing.T, db *sql.DB, url, eventType string, active bool) string {
	t.Helper()
	ctx := context.Background()
	var epID string
	err := db.QueryRowContext(ctx, `
		INSERT INTO endpoints (customer_id, url, secret, rate_limit_per_sec, active)
		VALUES ('test-customer', $1, 'test-secret', 10, $2) RETURNING id
	`, url, active).Scan(&epID)
	if err != nil {
		t.Fatalf("create test endpoint: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions (endpoint_id, event_type) VALUES ($1, $2)`, epID, eventType); err != nil {
		t.Fatalf("create test subscription: %v", err)
	}
	return epID
}

func TestInsertEventAndDeliveries(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	eventType := "test.event." + suffix
	idemKey := "idem-" + suffix

	// Three endpoints: subscribed+active (should get a delivery), subscribed+inactive (should
	// NOT), and active but subscribed to a different event type (should NOT). This is the actual
	// fan-out guarantee InsertEventAndDeliveries is supposed to provide.
	mustCreateEndpoint(t, db, "http://example.invalid/match-"+suffix, eventType, true)
	mustCreateEndpoint(t, db, "http://example.invalid/inactive-"+suffix, eventType, false)
	mustCreateEndpoint(t, db, "http://example.invalid/other-"+suffix, "some.other.event."+suffix, true)

	eventID, err := InsertEventAndDeliveries(ctx, db, "test", eventType, json.RawMessage(`{"ok":true}`), idemKey)
	if err != nil {
		t.Fatalf("InsertEventAndDeliveries: %v", err)
	}
	if eventID == "" {
		t.Fatal("expected a non-empty event id")
	}

	var deliveryCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE event_id = $1`, eventID).Scan(&deliveryCount); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveryCount != 1 {
		t.Fatalf("deliveries created = %d, want exactly 1 (only the active, subscribed endpoint)", deliveryCount)
	}

	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM deliveries WHERE event_id = $1`, eventID).Scan(&status); err != nil {
		t.Fatalf("read delivery status: %v", err)
	}
	if status != "pending" {
		t.Errorf("initial delivery status = %q, want %q", status, "pending")
	}

	// Re-ingesting the same idempotency key must not create a second event or duplicate deliveries.
	if _, err := InsertEventAndDeliveries(ctx, db, "test", eventType, json.RawMessage(`{"ok":true}`), idemKey); !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("second insert with same idempotency key: got err %v, want ErrDuplicateEvent", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE event_id = $1`, eventID).Scan(&deliveryCount); err != nil {
		t.Fatalf("count deliveries after duplicate insert: %v", err)
	}
	if deliveryCount != 1 {
		t.Fatalf("deliveries after duplicate insert = %d, want still 1", deliveryCount)
	}
}

func TestDeliveryLifecycleTransitions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	eventType := "test.lifecycle." + suffix

	mustCreateEndpoint(t, db, "http://example.invalid/lifecycle-"+suffix, eventType, true)

	eventID, err := InsertEventAndDeliveries(ctx, db, "test", eventType, json.RawMessage(`{}`), "idem-lifecycle-"+suffix)
	if err != nil {
		t.Fatalf("InsertEventAndDeliveries: %v", err)
	}
	var deliveryID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM deliveries WHERE event_id = $1`, eventID).Scan(&deliveryID); err != nil {
		t.Fatalf("find delivery: %v", err)
	}

	// pending -> failed, attempt_count incremented
	attempts, err := IncrementAttempt(ctx, db, deliveryID)
	if err != nil {
		t.Fatalf("IncrementAttempt: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts after first IncrementAttempt = %d, want 1", attempts)
	}
	if status, n, err := GetDeliveryStatus(ctx, db, deliveryID); err != nil {
		t.Fatalf("GetDeliveryStatus: %v", err)
	} else if status != "failed" || n != 1 {
		t.Errorf("after IncrementAttempt: status=%q attempts=%d, want failed/1", status, n)
	}

	// failed -> dead_letter
	if err := MarkDeadLetter(ctx, db, deliveryID); err != nil {
		t.Fatalf("MarkDeadLetter: %v", err)
	}
	if status, _, err := GetDeliveryStatus(ctx, db, deliveryID); err != nil {
		t.Fatalf("GetDeliveryStatus: %v", err)
	} else if status != "dead_letter" {
		t.Errorf("status after MarkDeadLetter = %q, want dead_letter", status)
	}

	// dead_letter -> pending via replay, attempt_count reset to 0 (this is the same path a
	// POST /replay/{id} call drives)
	endpointID, err := ResetForReplay(ctx, db, deliveryID)
	if err != nil {
		t.Fatalf("ResetForReplay: %v", err)
	}
	if endpointID == "" {
		t.Error("ResetForReplay returned an empty endpoint id")
	}
	if status, n, err := GetDeliveryStatus(ctx, db, deliveryID); err != nil {
		t.Fatalf("GetDeliveryStatus: %v", err)
	} else if status != "pending" || n != 0 {
		t.Errorf("after ResetForReplay: status=%q attempts=%d, want pending/0", status, n)
	}

	// pending -> succeeded
	if err := MarkSucceeded(ctx, db, deliveryID); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if status, _, err := GetDeliveryStatus(ctx, db, deliveryID); err != nil {
		t.Fatalf("GetDeliveryStatus: %v", err)
	} else if status != "succeeded" {
		t.Errorf("status after MarkSucceeded = %q, want succeeded", status)
	}
}

func TestGetDeliveryStatusNotFound(t *testing.T) {
	db := testDB(t)
	_, _, err := GetDeliveryStatus(context.Background(), db, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetDeliveryStatus for a missing id: got %v, want sql.ErrNoRows", err)
	}
}
