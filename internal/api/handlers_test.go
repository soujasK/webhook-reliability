package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIngestRejectsMalformedOrOversizedBodiesBeforeDatabase(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"malformed JSON", "{", http.StatusBadRequest},
		{"multiple JSON objects", `{"event_type":"a"} {"event_type":"b"}`, http.StatusBadRequest},
		{"trailing garbage", `{"event_type":"a"} trailing`, http.StatusBadRequest},
		{"oversized body", strings.Repeat(" ", maxIngestBodyBytes+1), http.StatusRequestEntityTooLarge},
	}
	server := &Server{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			server.handleIngestEvent(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestIngestDecodeStatus(t *testing.T) {
	if got := ingestDecodeStatus(nil); got != http.StatusBadRequest {
		t.Fatalf("nil error status = %d", got)
	}
}
