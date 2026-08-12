package queue

import (
	"context"
	"database/sql"
	"log"
	"time"

	"webhook-engine/internal/store"
)

// RunDispatcher is the durability safety net: if a process dies after the Postgres commit
// but before the Redis push, this loop re-feeds the delivery on its next tick instead of
// losing it. Redis is allowed to lose state; Postgres is not.
func RunDispatcher(ctx context.Context, db *sql.DB, redis *RedisClient, intervalMs int) {
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pending, err := store.FetchPendingDeliveries(ctx, db, 500)
			if err != nil {
				log.Printf("dispatcher: fetch pending failed: %v", err)
				continue
			}
			for _, p := range pending {
				if err := redis.LPushDelivery(p.EndpointID, p.DeliveryID); err != nil {
					log.Printf("dispatcher: lpush failed for %s: %v", p.DeliveryID, err)
					continue
				}
				if err := store.MarkQueued(ctx, db, p.DeliveryID); err != nil {
					log.Printf("dispatcher: mark queued failed for %s: %v", p.DeliveryID, err)
				}
			}
		}
	}
}
