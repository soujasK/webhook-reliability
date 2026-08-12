package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"webhook-engine/internal/queue"
	"webhook-engine/internal/store"
)

type Server struct {
	DB    *sql.DB
	Redis *queue.RedisClient
}

func NewServer(db *sql.DB, redis *queue.RedisClient) *Server {
	return &Server{DB: db, Redis: redis}
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /events", s.handleIngestEvent)
	mux.HandleFunc("POST /replay/{id}", s.handleReplay)
	mux.HandleFunc("GET /deliveries/{id}", s.handleGetDelivery)
	mux.Handle("/", http.FileServer(http.Dir("/frontend")))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

type ingestRequest struct {
	Source         string          `json:"source"`
	EventType      string          `json:"event_type"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// handleIngestEvent commits the event and every matching delivery row in one transaction
// before responding. This is the durability guarantee the whole system is built around.
func (s *Server) handleIngestEvent(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if req.EventType == "" || req.IdempotencyKey == "" {
		http.Error(w, "event_type and idempotency_key are required", http.StatusBadRequest)
		return
	}
	if req.Source == "" {
		req.Source = "unknown"
	}
	if req.Payload == nil {
		req.Payload = json.RawMessage("{}")
	}

	eventID, err := store.InsertEventAndDeliveries(r.Context(), s.DB, req.Source, req.EventType, req.Payload, req.IdempotencyKey)
	if errors.Is(err, store.ErrDuplicateEvent) {
		w.WriteHeader(http.StatusOK) // already ingested, nothing new to do
		w.Write([]byte(`{"status":"duplicate"}`))
		return
	}
	if err != nil {
		log.Printf("api: ingest failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "event_id": eventID})
}

// handleReplay resets a delivery to pending and re-queues it. Note it goes through the same
// durability path as a first attempt — status flips in Postgres, then Redis is pushed to.
func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	deliveryID := r.PathValue("id")
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}

	endpointID, err := store.ResetForReplay(r.Context(), s.DB, deliveryID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "delivery not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("api: replay failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.Redis.LPushDelivery(endpointID, deliveryID); err != nil {
		// Not fatal: the dispatcher's next sweep will pick up the 'pending' row anyway.
		log.Printf("api: replay lpush failed (dispatcher will retry): %v", err)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"replayed"}`))
}

func (s *Server) handleGetDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID := r.PathValue("id")
	status, attempts, err := store.GetDeliveryStatus(r.Context(), s.DB, deliveryID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "delivery not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("api: get delivery failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"delivery_id":   deliveryID,
		"status":        status,
		"attempt_count": attempts,
	})
}
