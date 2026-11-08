// Package store keeps short links and click statistics in PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

// ErrNotFound is returned when a short code does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a generated code already exists.
var ErrConflict = errors.New("code already exists")

// Link is a short link.
type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// DayCount is clicks for one day.
type DayCount struct {
	Day    string `json:"day"`
	Clicks int64  `json:"clicks"`
}

// Postgres implements the store with database/sql + lib/pq.
type Postgres struct {
	DB *sql.DB
}

// Open connects and configures a small pool suited to one Fargate task.
func Open(dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	return &Postgres{DB: db}, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS links (
    code       text PRIMARY KEY,
    url        text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS daily_clicks (
    code   text   NOT NULL REFERENCES links(code) ON DELETE CASCADE,
    day    date   NOT NULL,
    clicks bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (code, day)
);
CREATE INDEX IF NOT EXISTS links_created_idx ON links (created_at DESC);
`

// Migrate creates the schema. An advisory lock makes it safe when several
// tasks start at the same time during a deployment.
func (p *Postgres) Migrate(ctx context.Context) error {
	conn, err := p.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(424242)"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(424242)") //nolint:errcheck
	_, err = conn.ExecContext(ctx, schema)
	return err
}

// Ping checks the database.
func (p *Postgres) Ping(ctx context.Context) error { return p.DB.PingContext(ctx) }

// Create stores a new link.
func (p *Postgres) Create(ctx context.Context, code, url string) (Link, error) {
	var l Link
	err := p.DB.QueryRowContext(ctx,
		`INSERT INTO links (code, url) VALUES ($1, $2) RETURNING code, url, created_at`, code, url).
		Scan(&l.Code, &l.URL, &l.CreatedAt)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
		return Link{}, ErrConflict
	}
	return l, err
}

// URL returns the target of a short code.
func (p *Postgres) URL(ctx context.Context, code string) (string, error) {
	var url string
	err := p.DB.QueryRowContext(ctx, `SELECT url FROM links WHERE code = $1`, code).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return url, err
}

// Recent returns the newest links.
func (p *Postgres) Recent(ctx context.Context, limit int) ([]Link, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT code, url, created_at FROM links ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Code, &l.URL, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Daily returns processed clicks per day (newest first, last 30 days).
func (p *Postgres) Daily(ctx context.Context, code string) ([]DayCount, error) {
	rows, err := p.DB.QueryContext(ctx,
		`SELECT to_char(day, 'YYYY-MM-DD'), clicks FROM daily_clicks
		 WHERE code = $1 AND day >= current_date - 30 ORDER BY day DESC`, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayCount{}
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Clicks); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
