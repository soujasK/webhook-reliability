package queue

import (
	"fmt"
	"time"

	"github.com/gomodule/redigo/redis"
)

type RedisClient struct {
	Pool *redis.Pool
}

// NewRedisClient dials via a redis:// (or rediss:// for TLS) URL rather than a bare host:port,
// so hosted Redis that requires AUTH (Railway, Upstash, etc.) works the same as local dev.
func NewRedisClient(redisURL string) *RedisClient {
	pool := &redis.Pool{
		MaxIdle:     20,
		MaxActive:   100,
		IdleTimeout: 240 * time.Second,
		Dial:        func() (redis.Conn, error) { return redis.DialURL(redisURL) },
	}
	return &RedisClient{Pool: pool}
}

func queueKey(endpointID string) string { return "queue:" + endpointID }
func lockKey(endpointID string) string  { return "lock:endpoint:" + endpointID }

const retryZSetKey = "retry_zset"

func (r *RedisClient) LPushDelivery(endpointID, deliveryID string) error {
	conn := r.Pool.Get()
	defer conn.Close()
	_, err := conn.Do("LPUSH", queueKey(endpointID), deliveryID)
	return err
}

// BRPopDelivery blocks up to timeoutSec waiting for an item. Returns ("", nil) on timeout (empty queue).
func (r *RedisClient) BRPopDelivery(endpointID string, timeoutSec int) (string, error) {
	conn := r.Pool.Get()
	defer conn.Close()
	reply, err := redis.Strings(conn.Do("BRPOP", queueKey(endpointID), timeoutSec))
	if err == redis.ErrNil {
		return "", nil // queue empty, not an error
	}
	if err != nil {
		return "", err
	}
	// BRPOP replies [key, value]
	if len(reply) < 2 {
		return "", nil
	}
	return reply[1], nil
}

// TryLockEndpoint acquires a short-lived lock so only one worker drains a given endpoint's queue at a time.
func (r *RedisClient) TryLockEndpoint(endpointID string, ttlSec int) (bool, error) {
	conn := r.Pool.Get()
	defer conn.Close()
	reply, err := conn.Do("SET", lockKey(endpointID), "1", "NX", "EX", ttlSec)
	if err != nil {
		return false, err
	}
	return reply != nil, nil
}

func (r *RedisClient) UnlockEndpoint(endpointID string) error {
	conn := r.Pool.Get()
	defer conn.Close()
	_, err := conn.Do("DEL", lockKey(endpointID))
	return err
}

func (r *RedisClient) ScheduleRetry(deliveryID string, at time.Time) error {
	conn := r.Pool.Get()
	defer conn.Close()
	_, err := conn.Do("ZADD", retryZSetKey, at.Unix(), deliveryID)
	return err
}

// DueRetries returns delivery IDs scored at or before now, and removes them from the set.
func (r *RedisClient) DueRetries(now time.Time, limit int) ([]string, error) {
	conn := r.Pool.Get()
	defer conn.Close()
	ids, err := redis.Strings(conn.Do("ZRANGEBYSCORE", retryZSetKey, "0", now.Unix(), "LIMIT", 0, limit))
	if err != nil && err != redis.ErrNil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := conn.Do("ZREM", retryZSetKey, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

const tokenBucketScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local bucket = redis.call("HMGET", key, "tokens", "last_refill")
local tokens = tonumber(bucket[1])
local last_refill = tonumber(bucket[2])
if tokens == nil then tokens = capacity end
if last_refill == nil then last_refill = now end

local elapsed = now - last_refill
if elapsed < 0 then elapsed = 0 end
tokens = math.min(capacity, tokens + elapsed * refill_rate)

if tokens < 1 then
  redis.call("HMSET", key, "tokens", tokens, "last_refill", now)
  redis.call("EXPIRE", key, 60)
  return 0
end

tokens = tokens - 1
redis.call("HMSET", key, "tokens", tokens, "last_refill", now)
redis.call("EXPIRE", key, 60)
return 1
`

// AllowToken runs the token-bucket check+decrement atomically via a Lua script,
// so two goroutines racing on the same endpoint can't both slip through.
func (r *RedisClient) AllowToken(endpointID string, capacity, refillPerSec int) (bool, error) {
	conn := r.Pool.Get()
	defer conn.Close()
	key := fmt.Sprintf("bucket:%s", endpointID)
	reply, err := redis.Int(conn.Do("EVAL", tokenBucketScript, 1, key, capacity, refillPerSec, time.Now().Unix()))
	if err != nil {
		return false, err
	}
	return reply == 1, nil
}
