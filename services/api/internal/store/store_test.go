package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Integration test: runs when TEST_DATABASE_URL points to a disposable database
// (CI starts postgres:16 as a service container).
func TestPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.DB.Close()
	if err := p.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Migrate(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	code := "t" + time.Now().Format("150405999")
	if _, err := p.Create(ctx, code, "https://example.com/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(ctx, code, "https://example.com/y"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate code: want ErrConflict, got %v", err)
	}
	if u, err := p.URL(ctx, code); err != nil || u != "https://example.com/x" {
		t.Fatalf("URL = %q, %v", u, err)
	}
	if _, err := p.URL(ctx, "missing-code"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: want ErrNotFound, got %v", err)
	}
	if _, err := p.DB.ExecContext(ctx, `INSERT INTO daily_clicks (code, day, clicks) VALUES ($1, current_date, 4)`, code); err != nil {
		t.Fatal(err)
	}
	days, err := p.Daily(ctx, code)
	if err != nil || len(days) != 1 || days[0].Clicks != 4 {
		t.Fatalf("Daily = %v, %v", days, err)
	}
	recent, err := p.Recent(ctx, 5)
	if err != nil || len(recent) == 0 {
		t.Fatalf("Recent = %v, %v", recent, err)
	}
	if _, err := p.DB.ExecContext(ctx, `DELETE FROM links WHERE code = $1`, code); err != nil {
		t.Fatal(err)
	}
}
