package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
)

const maxRequestBodyBytes = 1 << 20

// newReceiverHandler simulates a configurable number of failures, counting only
// valid delivery attempts. A new handler gets an independent counter.
func newReceiverHandler(failCount int) http.Handler {
	if failCount < 0 {
		failCount = 0
	}
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/receive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid or oversized request body", http.StatusRequestEntityTooLarge)
			return
		}
		n := atomic.AddInt64(&hits, 1)
		log.Printf("mock-receiver: hit #%d, X-Event-Id=%s, body=%s", n, r.Header.Get("X-Event-Id"), string(body))
		if n <= int64(failCount) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("simulated failure"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// A deliberately unreliable receiver, used to exercise retries and dead-lettering.
// FAIL_COUNT (default 0) requests return 500 before the receiver starts returning 200.
func main() {
	failCount, err := strconv.Atoi(os.Getenv("FAIL_COUNT"))
	if err != nil || failCount < 0 {
		failCount = 0
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}
	log.Printf("mock-receiver listening on :%s (fails first %d requests)", port, failCount)
	log.Fatal(http.ListenAndServe(":"+port, newReceiverHandler(failCount)))
}
