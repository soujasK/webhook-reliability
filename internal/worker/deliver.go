package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"math/rand"
	"net/http"
	"time"

	"webhook-engine/internal/store"
)

func (p *Pool) deliverOne(ctx context.Context, deliveryID, endpointID string) {
	job, err := store.LoadDeliveryJob(ctx, p.db, deliveryID)
	if err != nil {
		log.Printf("worker: load delivery %s failed: %v", deliveryID, err)
		return
	}

	allowed, err := p.redis.AllowToken(endpointID, job.RateLimit, job.RateLimit)
	if err != nil {
		log.Printf("worker: rate limit check failed for %s: %v", endpointID, err)
	}
	if !allowed {
		time.Sleep(50 * time.Millisecond)
		if err := p.redis.LPushDelivery(endpointID, deliveryID); err != nil {
			log.Printf("worker: requeue after rate limit failed: %v", err)
		}
		return
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.URL, bytes.NewReader(job.Payload))
	if err != nil {
		log.Printf("worker: build request failed for %s: %v", deliveryID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Id", job.EventID)
	req.Header.Set("X-Signature", sign(job.Payload, job.Secret))

	resp, doErr := p.http.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())

	var statusCode *int
	errMsg := ""
	success := false
	if doErr != nil {
		errMsg = doErr.Error()
	} else {
		code := resp.StatusCode
		statusCode = &code
		success = code >= 200 && code < 300
		resp.Body.Close()
	}

	attemptNum := job.AttemptCount + 1
	if err := store.RecordAttempt(ctx, p.db, deliveryID, attemptNum, statusCode, errMsg, latencyMs); err != nil {
		log.Printf("worker: record attempt failed for %s: %v", deliveryID, err)
	}

	if success {
		if err := store.MarkSucceeded(ctx, p.db, deliveryID); err != nil {
			log.Printf("worker: mark succeeded failed for %s: %v", deliveryID, err)
		}
		log.Printf("delivered %s -> endpoint %s in %dms", deliveryID, endpointID, latencyMs)
		return
	}

	p.scheduleRetry(ctx, deliveryID)
}

func (p *Pool) scheduleRetry(ctx context.Context, deliveryID string) {
	attempts, err := store.IncrementAttempt(ctx, p.db, deliveryID)
	if err != nil {
		log.Printf("worker: increment attempt failed for %s: %v", deliveryID, err)
		return
	}
	if attempts >= p.maxAttempts {
		if err := store.MarkDeadLetter(ctx, p.db, deliveryID); err != nil {
			log.Printf("worker: mark dead letter failed for %s: %v", deliveryID, err)
		}
		log.Printf("dead-lettered %s after %d attempts", deliveryID, attempts)
		return
	}
	// Cap exponential backoff at one hour; large attempt counts must not overflow
	// time.Duration and accidentally schedule retries in the past.
	delay := retryBackoff(attempts)
	jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
	if err := p.redis.ScheduleRetry(deliveryID, time.Now().Add(delay+jitter)); err != nil {
		log.Printf("worker: schedule retry failed for %s: %v", deliveryID, err)
	}
	log.Printf("retry scheduled for %s: attempt %d, backoff ~%v", deliveryID, attempts, delay)
}

func sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// retryBackoff grows exponentially but never exceeds one hour. Invalid
// non-positive attempt counts use the first retry interval.
func retryBackoff(attempts int) time.Duration {
	if attempts <= 0 {
		attempts = 1
	}
	if attempts >= 12 {
		return time.Hour
	}
	seconds := int64(1) << uint(attempts)
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}
