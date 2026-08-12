package queue

import (
	"context"
	"database/sql"
	"log"
	"time"

	"webhook-engine/internal/store"
)

// RunRetryScheduler polls retry_zset for anything due and re-queues it. Sorted-set scoring
// means this never has to scan failed deliveries that aren't ready yet.
func RunRetryScheduler(ctx context.Context, db *sql.DB, redis *RedisClient, intervalMs int) {
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			due, err := redis.DueRetries(time.Now(), 500)
			if err != nil {
				log.Printf("scheduler: due retries failed: %v", err)
				continue
			}
			for _, deliveryID := range due {
				job, err := store.LoadDeliveryJob(ctx, db, deliveryID)
				if err != nil {
					log.Printf("scheduler: load delivery %s failed: %v", deliveryID, err)
					continue
				}
				if err := redis.LPushDelivery(job.EndpointID, deliveryID); err != nil {
					log.Printf("scheduler: requeue %s failed: %v", deliveryID, err)
				}
			}
		}
	}
}
