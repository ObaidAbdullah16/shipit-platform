// Package config reads settings from environment variables (12-factor style).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
)

// Config holds runtime settings.
type Config struct {
	Port            string
	DatabaseURL     string
	RedisAddr       string
	PublicBaseURL   string // optional; otherwise derived from the request Host
	ChaosFailHealth bool   // makes /healthz fail: used to demo automatic rollback
	CreatePerMinute int
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Load reads the environment. DATABASE_URL wins; otherwise DB_HOST/DB_USER/
// DB_PASSWORD/DB_NAME are combined (ECS injects DB_PASSWORD from Secrets Manager).
func Load() (Config, error) {
	c := Config{
		Port:          env("PORT", "8080"),
		RedisAddr:     env("REDIS_ADDR", "localhost:6379"),
		PublicBaseURL: os.Getenv("PUBLIC_BASE_URL"),
	}
	c.ChaosFailHealth = os.Getenv("CHAOS_FAIL_HEALTH") == "1"
	n, err := strconv.Atoi(env("CREATE_PER_MINUTE", "20"))
	if err != nil || n <= 0 {
		return c, fmt.Errorf("CREATE_PER_MINUTE must be a positive number")
	}
	c.CreatePerMinute = n
	c.DatabaseURL = os.Getenv("DATABASE_URL")
	if c.DatabaseURL == "" {
		host := os.Getenv("DB_HOST")
		if host == "" {
			return c, fmt.Errorf("set DATABASE_URL or DB_HOST/DB_USER/DB_PASSWORD/DB_NAME")
		}
		u := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(env("DB_USER", "shipit"), os.Getenv("DB_PASSWORD")),
			Host:     host + ":" + env("DB_PORT", "5432"),
			Path:     "/" + env("DB_NAME", "shipit"),
			RawQuery: "sslmode=" + env("DB_SSLMODE", "require"),
		}
		c.DatabaseURL = u.String()
	}
	return c, nil
}
