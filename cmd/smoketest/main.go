// Command smoketest exercises a running webhook-engine deployment from the outside: it hits the
// public HTTP API the way a real caller would, and (optionally, if it can reach Postgres) reads
// back delivery outcomes to report the reliability metrics that actually matter for a system
// like this — see the "Metrics" section this prints for what and why.
//
// Usage:
//
//	go run ./cmd/smoketest -base-url=http://localhost:8080 -events=20
//
// Requires a demo endpoint already registered and subscribed to -event-type (see README's
// --mode=seed, or set SEED_URL on the server) — otherwise ingestion still works but nothing gets
// delivered, and the load-test phase will report 0 deliveries with a warning.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"time"

	_ "github.com/lib/pq"
)

func main() {
	baseURL := flag.String("base-url", envOr("BASE_URL", "http://localhost:8080"), "API base URL")
	dbURL := flag.String("database-url", envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"), "Postgres DSN used only to look up delivery IDs/outcomes for richer metrics; leave unreachable to skip that and get ingest-only results")
	eventType := flag.String("event-type", "payment.succeeded", "event_type to fire; must match a subscribed endpoint for the load phase to observe deliveries")
	numEvents := flag.Int("events", 20, "number of events to fire in the load phase")
	perDeliveryTimeout := flag.Duration("timeout", 20*time.Second, "how long to wait for each delivery to reach a terminal state")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}
	failed := false

	fmt.Println("== webhook-engine smoke test ==")
	fmt.Printf("target: %s\n\n", *baseURL)

	// --- Phase 1: liveness --------------------------------------------------------------
	fmt.Print("[1/4] GET /healthz ... ")
	if err := checkHealth(client, *baseURL); err != nil {
		fmt.Printf("FAIL (%v)\n", err)
		fmt.Println("\naborting: the API isn't reachable, nothing else will work either.")
		os.Exit(1)
	}
	fmt.Println("ok")

	// --- Phase 2: idempotency -------------------------------------------------------------
	fmt.Print("[2/4] idempotency dedup (same idempotency_key posted twice) ... ")
	if err := checkIdempotency(client, *baseURL); err != nil {
		fmt.Printf("FAIL (%v)\n", err)
		failed = true
	} else {
		fmt.Println("ok")
	}

	// --- Phase 3: not-found path -----------------------------------------------------------
	fmt.Print("[3/4] POST /replay/{unknown-id} returns 404 ... ")
	if err := checkReplayNotFound(client, *baseURL); err != nil {
		fmt.Printf("FAIL (%v)\n", err)
		failed = true
	} else {
		fmt.Println("ok")
	}

	// --- Phase 4: load + reliability metrics ------------------------------------------------
	fmt.Printf("[4/4] firing %d events (event_type=%s) and tracking outcomes ...\n", *numEvents, *eventType)
	db, dbErr := sql.Open("postgres", *dbURL)
	if dbErr == nil {
		dbErr = db.Ping()
	}
	if dbErr != nil {
		fmt.Printf("      (no DB access at %s: %v — will report ingest latency only, not delivery outcomes)\n", *dbURL, dbErr)
		db = nil
	}

	report := runLoad(client, db, *baseURL, *eventType, *numEvents, *perDeliveryTimeout)
	report.print()

	fmt.Println()
	if failed {
		fmt.Println("RESULT: FAIL — one or more correctness checks failed, see above.")
		os.Exit(1)
	}
	fmt.Println("RESULT: PASS (correctness checks). See metrics above for delivery-outcome details.")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type ingestResp struct {
	Status  string `json:"status"`
	EventID string `json:"event_id"`
}

type deliveryResp struct {
	DeliveryID   string `json:"delivery_id"`
	Status       string `json:"status"`
	AttemptCount int    `json:"attempt_count"`
}

func postEvent(client *http.Client, baseURL, eventType, idemKey string) (*http.Response, ingestResp, error) {
	body, _ := json.Marshal(map[string]any{
		"source":          "smoketest",
		"event_type":      eventType,
		"payload":         map[string]any{"smoketest": true, "sent_at": time.Now().Format(time.RFC3339Nano)},
		"idempotency_key": idemKey,
	})
	resp, err := client.Post(baseURL+"/events", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, ingestResp{}, err
	}
	defer resp.Body.Close()
	var out ingestResp
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp, out, nil
}

func checkHealth(client *http.Client, baseURL string) error {
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func checkIdempotency(client *http.Client, baseURL string) error {
	key := fmt.Sprintf("smoketest-idem-%d", time.Now().UnixNano())
	resp1, out1, err := postEvent(client, baseURL, "smoketest.idempotency", key)
	if err != nil {
		return err
	}
	if resp1.StatusCode != http.StatusAccepted || out1.Status != "accepted" || out1.EventID == "" {
		return fmt.Errorf("first post: status=%d body=%+v, want 202 accepted with an event_id", resp1.StatusCode, out1)
	}

	resp2, out2, err := postEvent(client, baseURL, "smoketest.idempotency", key)
	if err != nil {
		return err
	}
	if resp2.StatusCode != http.StatusOK || out2.Status != "duplicate" {
		return fmt.Errorf("second post (same key): status=%d body=%+v, want 200 duplicate", resp2.StatusCode, out2)
	}
	return nil
}

func checkReplayNotFound(client *http.Client, baseURL string) error {
	resp, err := client.Post(baseURL+"/replay/00000000-0000-0000-0000-000000000000", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("status %d, want 404", resp.StatusCode)
	}
	return nil
}

type outcome struct {
	ingestLatency time.Duration
	e2eLatency    time.Duration // ingest -> terminal delivery state; zero if no DB or timed out
	finalStatus   string        // succeeded | dead_letter | pending (timed out) | "" (no delivery / no DB)
	attempts      int
}

type loadReport struct {
	eventsFired    int
	deliveriesSeen int
	outcomes       []outcome
	noDeliveryRows int // events for which no delivery row was found at all (no subscribed endpoint)
}

func runLoad(client *http.Client, db *sql.DB, baseURL, eventType string, n int, perDeliveryTimeout time.Duration) *loadReport {
	report := &loadReport{eventsFired: n}
	runID := time.Now().UnixNano()

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("smoketest-load-%d-%d", runID, i)
		start := time.Now()
		resp, out, err := postEvent(client, baseURL, eventType, key)
		ingestLatency := time.Since(start)
		if err != nil || resp.StatusCode != http.StatusAccepted {
			log.Printf("      event %d/%d: ingest failed (err=%v status=%v)", i+1, n, err, safeStatus(resp))
			continue
		}

		if db == nil {
			report.outcomes = append(report.outcomes, outcome{ingestLatency: ingestLatency})
			continue
		}

		deliveryID, ok := findDelivery(db, out.EventID)
		if !ok {
			report.noDeliveryRows++
			continue
		}
		report.deliveriesSeen++

		status, attempts := pollDelivery(client, baseURL, deliveryID, perDeliveryTimeout)
		report.outcomes = append(report.outcomes, outcome{
			ingestLatency: ingestLatency,
			e2eLatency:    time.Since(start),
			finalStatus:   status,
			attempts:      attempts,
		})

		// Small stagger so this doesn't fire faster than a single-endpoint rate limit allows,
		// which would just pile everything into retry backoff and make the run slow to read.
		time.Sleep(time.Duration(20+rand.Intn(30)) * time.Millisecond)
	}
	return report
}

func safeStatus(resp *http.Response) any {
	if resp == nil {
		return nil
	}
	return resp.StatusCode
}

func findDelivery(db *sql.DB, eventID string) (string, bool) {
	if eventID == "" {
		return "", false
	}
	var id string
	err := db.QueryRow(`SELECT id FROM deliveries WHERE event_id = $1 LIMIT 1`, eventID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		log.Printf("      warning: delivery lookup failed for event %s: %v", eventID, err)
		return "", false
	}
	return id, true
}

func pollDelivery(client *http.Client, baseURL, deliveryID string, timeout time.Duration) (status string, attempts int) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/deliveries/" + deliveryID)
		if err == nil && resp.StatusCode == http.StatusOK {
			var d deliveryResp
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			_ = json.Unmarshal(raw, &d)
			if d.Status == "succeeded" || d.Status == "dead_letter" {
				return d.Status, d.AttemptCount
			}
			status, attempts = d.Status, d.AttemptCount
		} else if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status == "" {
		status = "unknown"
	}
	return status, attempts // still not terminal — reported as a timeout below
}

func (r *loadReport) print() {
	var ingestLat, e2eLat []time.Duration
	var succeeded, deadLettered, retried, timedOut int

	for _, o := range r.outcomes {
		ingestLat = append(ingestLat, o.ingestLatency)
		switch o.finalStatus {
		case "succeeded":
			succeeded++
			e2eLat = append(e2eLat, o.e2eLatency)
		case "dead_letter":
			deadLettered++
			e2eLat = append(e2eLat, o.e2eLatency)
		case "":
			// no DB access, outcome unknown
		default:
			timedOut++
		}
		if o.attempts > 1 {
			retried++
		}
	}

	fmt.Println()
	fmt.Println("---- metrics ----")
	fmt.Printf("events fired:                %d\n", r.eventsFired)
	if r.noDeliveryRows > 0 {
		fmt.Printf("WARNING: %d event(s) had no delivery row at all — is anything subscribed to this event_type? (see --mode=seed)\n", r.noDeliveryRows)
	}
	fmt.Printf("ingest latency (API 202):    p50=%s p95=%s max=%s\n", pct(ingestLat, 50), pct(ingestLat, 95), pct(ingestLat, 100))

	if len(r.outcomes) == 0 || allEmptyStatus(r.outcomes) {
		fmt.Println("(no Postgres access — delivery-outcome metrics below are unavailable; pass -database-url to see them)")
		return
	}

	total := succeeded + deadLettered + timedOut
	fmt.Printf("delivery success rate:       %s  (%d/%d succeeded)\n", pctStr(succeeded, total), succeeded, total)
	fmt.Printf("retry rate:                  %s  (%d/%d needed attempt_count > 1)\n", pctStr(retried, total), retried, total)
	fmt.Printf("dead-letter rate:            %s  (%d/%d exhausted MAX_ATTEMPTS)\n", pctStr(deadLettered, total), deadLettered, total)
	if timedOut > 0 {
		fmt.Printf("still pending at timeout:    %d  (dispatcher/worker may be behind or down — re-run with a longer -timeout)\n", timedOut)
	}
	fmt.Printf("end-to-end latency (ingest→terminal): p50=%s p95=%s max=%s\n", pct(e2eLat, 50), pct(e2eLat, 95), pct(e2eLat, 100))
}

func allEmptyStatus(outcomes []outcome) bool {
	for _, o := range outcomes {
		if o.finalStatus != "" {
			return false
		}
	}
	return true
}

func pct(durs []time.Duration, p int) string {
	if len(durs) == 0 {
		return "n/a"
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p * (len(sorted) - 1)) / 100
	return sorted[idx].Round(time.Millisecond).String()
}

func pctStr(n, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(total))
}
