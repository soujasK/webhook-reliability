package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
)

// A deliberately unreliable receiver, used to exercise retries and dead-lettering.
// FAIL_COUNT (default 0) requests return 500 before the receiver starts returning 200.
func main() {
	failCount, _ := strconv.Atoi(os.Getenv("FAIL_COUNT"))
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}

	var hits int32

	http.HandleFunc("/receive", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := atomic.AddInt32(&hits, 1)
		log.Printf("mock-receiver: hit #%d, X-Event-Id=%s, body=%s", n, r.Header.Get("X-Event-Id"), string(body))

		if int(n) <= failCount {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("simulated failure"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("mock-receiver listening on :%s (fails first %d requests)", port, failCount)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
