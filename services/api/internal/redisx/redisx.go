// Package redisx is a deliberately tiny Redis client (RESP2) covering only the
// commands the API needs. Keeping it in-house keeps the API at one third-party
// dependency (the Postgres driver), which shrinks the supply-chain surface.
package redisx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// ErrNil is returned when Redis replies with a nil bulk string (key not found).
var ErrNil = errors.New("redis: nil")

// Client is a small connection pool.
type Client struct {
	Addr    string
	Timeout time.Duration

	mu   sync.Mutex
	idle []*conn
	max  int
}

type conn struct {
	c net.Conn
	r *bufio.Reader
}

// New creates a client for addr (host:port).
func New(addr string) *Client {
	return &Client{Addr: addr, Timeout: 2 * time.Second, max: 8}
}

func (c *Client) get(ctx context.Context) (*conn, error) {
	c.mu.Lock()
	if n := len(c.idle); n > 0 {
		cn := c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		return cn, nil
	}
	c.mu.Unlock()
	d := net.Dialer{Timeout: c.Timeout}
	nc, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, err
	}
	return &conn{c: nc, r: bufio.NewReader(nc)}, nil
}

func (c *Client) put(cn *conn, broken bool) {
	if broken {
		cn.c.Close()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.idle) >= c.max {
		cn.c.Close()
		return
	}
	c.idle = append(c.idle, cn)
}

// Close closes idle connections.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cn := range c.idle {
		cn.c.Close()
	}
	c.idle = nil
}

// Do sends one command and returns the parsed reply:
// string, int64, nil (ErrNil), []any or an error reply.
func (c *Client) Do(ctx context.Context, args ...string) (any, error) {
	cn, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(c.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = cn.c.SetDeadline(deadline)
	if _, err := cn.c.Write(encode(args)); err != nil {
		c.put(cn, true)
		return nil, err
	}
	v, err := readReply(cn.r)
	var re RedisError
	broken := err != nil && !errors.As(err, &re) && !errors.Is(err, ErrNil)
	c.put(cn, broken)
	return v, err
}

func encode(args []string) []byte {
	b := []byte("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b = append(b, '$')
		b = strconv.AppendInt(b, int64(len(a)), 10)
		b = append(b, '\r', '\n')
		b = append(b, a...)
		b = append(b, '\r', '\n')
	}
	return b
}

// RedisError is an error reply ("-ERR ...") from the server.
type RedisError string

func (e RedisError) Error() string { return "redis: " + string(e) }

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return "", fmt.Errorf("redis: malformed line %q", line)
	}
	return line[:len(line)-2], nil
}

func readReply(r *bufio.Reader) (any, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if line == "" {
		return nil, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, RedisError(line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, ErrNil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, ErrNil
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := readReply(r)
			if err != nil && !errors.Is(err, ErrNil) {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("redis: unknown reply type %q", line[0])
}

// Ping checks the connection.
func (c *Client) Ping(ctx context.Context) error {
	v, err := c.Do(ctx, "PING")
	if err != nil {
		return err
	}
	if v != "PONG" {
		return fmt.Errorf("redis: unexpected PING reply %v", v)
	}
	return nil
}

// Get returns the value of key or ErrNil.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	v, err := c.Do(ctx, "GET", key)
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}

// SetEX sets key with a time-to-live.
func (c *Client) SetEX(ctx context.Context, key, value string, ttl time.Duration) error {
	_, err := c.Do(ctx, "SET", key, value, "EX", strconv.Itoa(int(ttl.Seconds())))
	return err
}

// LPush pushes a value onto a list and returns the new length.
func (c *Client) LPush(ctx context.Context, key, value string) (int64, error) {
	v, err := c.Do(ctx, "LPUSH", key, value)
	if err != nil {
		return 0, err
	}
	n, _ := v.(int64)
	return n, nil
}

// Incr increments a counter.
func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	v, err := c.Do(ctx, "INCR", key)
	if err != nil {
		return 0, err
	}
	n, _ := v.(int64)
	return n, nil
}

// GetInt returns an integer value (0 when missing).
func (c *Client) GetInt(ctx context.Context, key string) (int64, error) {
	s, err := c.Get(ctx, key)
	if errors.Is(err, ErrNil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}
