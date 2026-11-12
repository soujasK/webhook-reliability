package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReceiverFailsFirstNValidDeliveries(t *testing.T) {
	h := newReceiverHandler(2)
	for i, want := range []int{500, 500, 200, 200} {
		req := httptest.NewRequest(http.MethodPost, "/receive", strings.NewReader(`{"event":1}`))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("attempt %d: got %d, want %d", i+1, res.Code, want)
		}
	}
}

func TestReceiverRejectsInvalidRequestsWithoutConsumingFailure(t *testing.T) {
	h := newReceiverHandler(1)
	tests := []struct {
		method, body string
		want         int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, strings.Repeat("x", maxRequestBodyBytes+1), http.StatusRequestEntityTooLarge},
		{http.MethodPost, "ok", http.StatusInternalServerError},
		{http.MethodPost, "ok", http.StatusOK},
	}
	for i, tt := range tests {
		res := httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest(tt.method, "/receive", strings.NewReader(tt.body)))
		if res.Code != tt.want {
			t.Fatalf("case %d: got %d, want %d", i, res.Code, tt.want)
		}
	}
}

func TestReceiverNegativeFailureCount(t *testing.T) {
	res := httptest.NewRecorder()
	newReceiverHandler(-5).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/receive", strings.NewReader("ok")))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", res.Code)
	}
}
