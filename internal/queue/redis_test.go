package queue

import (
	"reflect"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
)

type retryTestConn struct {
	commands []string
}

func (c *retryTestConn) Close() error { return nil }
func (c *retryTestConn) Err() error { return nil }
func (c *retryTestConn) Send(name string, args ...interface{}) error { return nil }
func (c *retryTestConn) Flush() error { return nil }
func (c *retryTestConn) Receive() (interface{}, error) { return nil, nil }
func (c *retryTestConn) Do(name string, args ...interface{}) (interface{}, error) {
	c.commands = append(c.commands, name)
	if name == "ZRANGEBYSCORE" {
		return []interface{}{[]byte("delivery-1")}, nil
	}
	return int64(1), nil
}

func TestDueRetriesDoesNotDeleteBeforeRequeue(t *testing.T) {
	conn := &retryTestConn{}
	client := &RedisClient{Pool: &redis.Pool{
		Dial: func() (redis.Conn, error) { return conn, nil },
	}}
	ids, err := client.DueRetries(time.Unix(100, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"delivery-1"}) {
		t.Fatalf("due IDs = %v", ids)
	}
	if !reflect.DeepEqual(conn.commands, []string{"ZRANGEBYSCORE"}) {
		t.Fatalf("reading due retries must not remove them: %v", conn.commands)
	}
	if err := client.AckRetry("delivery-1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conn.commands, []string{"ZRANGEBYSCORE", "ZREM"}) {
		t.Fatalf("acknowledgement must remove the retry: %v", conn.commands)
	}
}
