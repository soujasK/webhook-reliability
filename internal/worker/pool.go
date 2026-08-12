package worker

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"webhook-engine/internal/queue"
	"webhook-engine/internal/store"
)

type Pool struct {
	db          *sql.DB
	redis       *queue.RedisClient
	http        *http.Client
	maxAttempts int

	mu        sync.RWMutex
	endpoints []string
	counter   uint64
}

func NewPool(db *sql.DB, redis *queue.RedisClient, maxAttempts int) *Pool {
	return &Pool{
		db:          db,
		redis:       redis,
		http:        &http.Client{Timeout: 10 * time.Second},
		maxAttempts: maxAttempts,
	}
}

// Run starts numWorkers goroutines. Each one repeatedly claims whichever endpoint round-robin
// hands it next, drains that endpoint's queue completely, then moves on.
func (p *Pool) Run(ctx context.Context, numWorkers int) {
	go p.refreshEndpoints(ctx)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			p.workerLoop(ctx, workerID)
		}(i)
	}
	wg.Wait()
}

func (p *Pool) refreshEndpoints(ctx context.Context) {
	p.loadEndpoints(ctx)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.loadEndpoints(ctx)
		}
	}
}

func (p *Pool) loadEndpoints(ctx context.Context) {
	ids, err := store.ActiveEndpointIDs(ctx, p.db)
	if err != nil {
		log.Printf("worker: refresh endpoints failed: %v", err)
		return
	}
	p.mu.Lock()
	p.endpoints = ids
	p.mu.Unlock()
}

func (p *Pool) nextEndpoint() (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.endpoints) == 0 {
		return "", false
	}
	i := atomic.AddUint64(&p.counter, 1)
	return p.endpoints[i%uint64(len(p.endpoints))], true
}

func (p *Pool) workerLoop(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		endpointID, ok := p.nextEndpoint()
		if !ok {
			time.Sleep(500 * time.Millisecond) // nothing to do yet, back off briefly
			continue
		}
		p.drainEndpoint(ctx, endpointID)
	}
}

// drainEndpoint is where per-endpoint ordering comes from: only the goroutine holding the
// lock may pop from this endpoint's list, and BRPOP is strictly FIFO.
func (p *Pool) drainEndpoint(ctx context.Context, endpointID string) {
	locked, err := p.redis.TryLockEndpoint(endpointID, 30)
	if err != nil {
		log.Printf("worker: lock error for %s: %v", endpointID, err)
		return
	}
	if !locked {
		return // another worker already owns this endpoint right now
	}
	defer p.redis.UnlockEndpoint(endpointID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		deliveryID, err := p.redis.BRPopDelivery(endpointID, 1)
		if err != nil {
			log.Printf("worker: brpop error for %s: %v", endpointID, err)
			return
		}
		if deliveryID == "" {
			return // queue empty, release the lock and let the caller move on
		}
		p.deliverOne(ctx, deliveryID, endpointID)
	}
}
