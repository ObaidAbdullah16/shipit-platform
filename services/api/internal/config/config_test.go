package config

import (
	"strings"
	"testing"
)

func TestLoadFromParts(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_PASSWORD", "p@ss/word")
	t.Setenv("CHAOS_FAIL_HEALTH", "1")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.DatabaseURL, "postgres://shipit:p%40ss%2Fword@db.internal:5432/shipit?sslmode=require") {
		t.Fatalf("DatabaseURL = %s", c.DatabaseURL)
	}
	if !c.ChaosFailHealth || c.Port != "8080" || c.CreatePerMinute != 20 {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error without database settings")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("CREATE_PER_MINUTE", "zero")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad CREATE_PER_MINUTE")
	}
}
