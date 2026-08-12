package models

import "time"

type Event struct {
	ID             string
	Source         string
	EventType      string
	Payload        []byte
	IdempotencyKey string
	ReceivedAt     time.Time
}

type Endpoint struct {
	ID         string
	CustomerID string
	URL        string
	Secret     string
	RateLimit  int
	Active     bool
}

// DeliveryJob is the joined view a worker needs to actually perform a delivery:
// enough of the event + endpoint to send the HTTP request, without extra queries.
type DeliveryJob struct {
	DeliveryID   string
	EventID      string
	EndpointID   string
	Payload      []byte
	URL          string
	Secret       string
	RateLimit    int
	AttemptCount int
}

type DeliveryStatus string

const (
	StatusPending    DeliveryStatus = "pending"
	StatusQueued     DeliveryStatus = "queued"
	StatusSucceeded  DeliveryStatus = "succeeded"
	StatusFailed     DeliveryStatus = "failed"
	StatusDeadLetter DeliveryStatus = "dead_letter"
)
