package redisx

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEncode(t *testing.T) {
	got := string(encode([]string{"SET", "k", "héllo"}))
	want := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$6\r\nhéllo\r\n"
	if got != want {
		t.Fatalf("encode = %q, want %q", got, want)
	}
}

func TestReadReply(t *testing.T) {
	cases := []struct {
		in   string
		want any
		err  error
	}{
		{"+OK\r\n", "OK", nil},
		{":42\r\n", int64(42), nil},
		{"$5\r\nhello\r\n", "hello", nil},
		{"$-1\r\n", nil, ErrNil},
		{"*2\r\n$1\r\na\r\n:7\r\n", []any{"a", int64(7)}, nil},
	}
	for _, c := range cases {
		v, err := readReply(bufio.NewReader(strings.NewReader(c.in)))
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Fatalf("%q: err = %v, want %v", c.in, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: unexpected err %v", c.in, err)
		}
		if arr, ok := c.want.([]any); ok {
			got := v.([]any)
			if len(got) != len(arr) || got[0] != arr[0] || got[1] != arr[1] {
				t.Fatalf("%q: got %v", c.in, got)
			}
			continue
		}
		if v != c.want {
			t.Fatalf("%q: got %v want %v", c.in, v, c.want)
		}
	}
	_, err := readReply(bufio.NewReader(strings.NewReader("-ERR wrong type\r\n")))
	var re RedisError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "wrong type") {
		t.Fatalf("error reply not parsed: %v", err)
	}
}

// TestAgainstRealRedis runs when REDIS_ADDR points to a disposable Redis
// (CI starts one as a service container; locally: docker compose up redis).
func TestAgainstRealRedis(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err != nil {
		t.Skipf("redis not reachable: %v", err)
	} else {
		c.Close()
	}
	ctx := context.Background()
	c := New(addr)
	defer c.Close()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	key := "redisx-test:" + time.Now().Format("150405.000000")
	if _, err := c.Get(ctx, key); !errors.Is(err, ErrNil) {
		t.Fatalf("missing key: want ErrNil, got %v", err)
	}
	if err := c.SetEX(ctx, key, "v1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(ctx, key); err != nil || v != "v1" {
		t.Fatalf("get = %q, %v", v, err)
	}
	if n, err := c.GetInt(ctx, key+":n"); err != nil || n != 0 {
		t.Fatalf("GetInt missing = %d, %v", n, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := c.Incr(ctx, key+":n"); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := c.GetInt(ctx, key+":n"); n != 3 {
		t.Fatalf("counter = %d, want 3", n)
	}
	if n, err := c.LPush(ctx, key+":list", "a"); err != nil || n != 1 {
		t.Fatalf("lpush = %d, %v", n, err)
	}
	// An error reply must not poison the pooled connection.
	if _, err := c.Incr(ctx, key); err == nil {
		t.Fatal("INCR on a string should fail")
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("connection unusable after error reply: %v", err)
	}
	_, _ = c.Do(ctx, "DEL", key, key+":n", key+":list")
}
