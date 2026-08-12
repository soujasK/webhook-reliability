package store

import (
	"context"
	"database/sql"
	"errors"

	"webhook-engine/internal/models"
)

var ErrDuplicateEvent = errors.New("event already ingested")

// InsertEventAndDeliveries is the single-transaction write described in the architecture doc:
// the event and every matching delivery row land together, or none of them do.
func InsertEventAndDeliveries(ctx context.Context, db *sql.DB, source, eventType string, payload []byte, idempotencyKey string) (eventID string, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() // no-op once committed

	err = tx.QueryRowContext(ctx, `
		INSERT INTO events (source, event_type, payload, idempotency_key)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`, source, eventType, payload, idempotencyKey).Scan(&eventID)

	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDuplicateEvent
	}
	if err != nil {
		return "", err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT e.id FROM endpoints e
		JOIN subscriptions s ON s.endpoint_id = e.id
		WHERE s.event_type = $1 AND e.active = true
	`, eventType)
	if err != nil {
		return "", err
	}
	var endpointIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		endpointIDs = append(endpointIDs, id)
	}
	rows.Close()

	for _, epID := range endpointIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deliveries (event_id, endpoint_id, status)
			VALUES ($1, $2, 'pending')
		`, eventID, epID); err != nil {
			return "", err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return eventID, nil
}

type PendingRef struct {
	DeliveryID string
	EndpointID string
}

func FetchPendingDeliveries(ctx context.Context, db *sql.DB, limit int) ([]PendingRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, endpoint_id FROM deliveries WHERE status = 'pending' LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRef
	for rows.Next() {
		var r PendingRef
		if err := rows.Scan(&r.DeliveryID, &r.EndpointID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func MarkQueued(ctx context.Context, db *sql.DB, deliveryID string) error {
	_, err := db.ExecContext(ctx, `UPDATE deliveries SET status = 'queued', updated_at = now() WHERE id = $1`, deliveryID)
	return err
}

func LoadDeliveryJob(ctx context.Context, db *sql.DB, deliveryID string) (*models.DeliveryJob, error) {
	job := &models.DeliveryJob{DeliveryID: deliveryID}
	err := db.QueryRowContext(ctx, `
		SELECT d.event_id, d.endpoint_id, e.payload, ep.url, ep.secret, ep.rate_limit_per_sec, d.attempt_count
		FROM deliveries d
		JOIN events e ON e.id = d.event_id
		JOIN endpoints ep ON ep.id = d.endpoint_id
		WHERE d.id = $1
	`, deliveryID).Scan(&job.EventID, &job.EndpointID, &job.Payload, &job.URL, &job.Secret, &job.RateLimit, &job.AttemptCount)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func MarkSucceeded(ctx context.Context, db *sql.DB, deliveryID string) error {
	_, err := db.ExecContext(ctx, `UPDATE deliveries SET status = 'succeeded', updated_at = now() WHERE id = $1`, deliveryID)
	return err
}

// IncrementAttempt bumps attempt_count, sets status back to 'failed', and returns the new count
// so the caller can decide whether to retry or dead-letter.
func IncrementAttempt(ctx context.Context, db *sql.DB, deliveryID string) (int, error) {
	var attempts int
	err := db.QueryRowContext(ctx, `
		UPDATE deliveries SET attempt_count = attempt_count + 1, status = 'failed', updated_at = now()
		WHERE id = $1 RETURNING attempt_count
	`, deliveryID).Scan(&attempts)
	return attempts, err
}

func MarkDeadLetter(ctx context.Context, db *sql.DB, deliveryID string) error {
	_, err := db.ExecContext(ctx, `UPDATE deliveries SET status = 'dead_letter', updated_at = now() WHERE id = $1`, deliveryID)
	return err
}

func RecordAttempt(ctx context.Context, db *sql.DB, deliveryID string, attemptNumber int, httpStatus *int, errMsg string, latencyMs int) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO delivery_attempts (delivery_id, attempt_number, http_status, error, latency_ms)
		VALUES ($1, $2, $3, $4, $5)
	`, deliveryID, attemptNumber, httpStatus, nullIfEmpty(errMsg), latencyMs)
	return err
}

// ResetForReplay puts a delivery back to pending (attempt count cleared) and returns its
// endpoint_id so the caller can re-queue it. Uses the same durability path as first ingestion.
func ResetForReplay(ctx context.Context, db *sql.DB, deliveryID string) (string, error) {
	var endpointID string
	err := db.QueryRowContext(ctx, `
		UPDATE deliveries SET status = 'pending', attempt_count = 0, updated_at = now()
		WHERE id = $1 RETURNING endpoint_id
	`, deliveryID).Scan(&endpointID)
	return endpointID, err
}

func ActiveEndpointIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM endpoints WHERE active = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetDeliveryStatus is used by the read-only GET /deliveries/{id} endpoint.
func GetDeliveryStatus(ctx context.Context, db *sql.DB, deliveryID string) (status string, attempts int, err error) {
	err = db.QueryRowContext(ctx, `SELECT status, attempt_count FROM deliveries WHERE id = $1`, deliveryID).Scan(&status, &attempts)
	return
}

// SeedDemoEndpoint inserts a single endpoint + subscription for local testing (idempotent-ish via a fixed id check).
func SeedDemoEndpoint(ctx context.Context, db *sql.DB, url string) (endpointID string, err error) {
	var existing string
	err = db.QueryRowContext(ctx, `SELECT id FROM endpoints WHERE url = $1 LIMIT 1`, url).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = db.QueryRowContext(ctx, `
		INSERT INTO endpoints (customer_id, url, secret, rate_limit_per_sec, active)
		VALUES ('demo-customer', $1, 'demo-secret', 10, true) RETURNING id
	`, url).Scan(&endpointID)
	if err != nil {
		return "", err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO subscriptions (endpoint_id, event_type) VALUES ($1, 'payment.succeeded')
		ON CONFLICT DO NOTHING
	`, endpointID)
	return endpointID, err
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
