package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr           string
	DBPath             string
	ServiceToken       string
	BusyTimeout        time.Duration
	RecentHistoryDays  int
	ContextTokenBudget int
	WorkerInterval     time.Duration
	ReferenceTime      *time.Time
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:           envOr("VANI_HTTP_ADDR", "127.0.0.1:8080"),
		DBPath:             envOr("VANI_DB_PATH", "./data/vani.sqlite"),
		BusyTimeout:        5 * time.Second,
		RecentHistoryDays:  15,
		ContextTokenBudget: 900,
		WorkerInterval:     2 * time.Second,
	}
	cfg.ServiceToken = strings.TrimSpace(os.Getenv("VANI_SERVICE_TOKEN"))
	if cfg.ServiceToken == "" {
		return Config{}, errors.New("VANI_SERVICE_TOKEN is required")
	}
	if len(cfg.ServiceToken) < 16 {
		return Config{}, errors.New("VANI_SERVICE_TOKEN must contain at least 16 characters")
	}
	if value := strings.TrimSpace(os.Getenv("VANI_DB_BUSY_TIMEOUT_MS")); value != "" {
		ms, err := strconv.Atoi(value)
		if err != nil || ms < 100 || ms > 30000 {
			return Config{}, fmt.Errorf("VANI_DB_BUSY_TIMEOUT_MS must be an integer from 100 to 30000")
		}
		cfg.BusyTimeout = time.Duration(ms) * time.Millisecond
	}
	if value := strings.TrimSpace(os.Getenv("VANI_RECENT_HISTORY_DAYS")); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil || days < 1 || days > 365 {
			return Config{}, fmt.Errorf("VANI_RECENT_HISTORY_DAYS must be an integer from 1 to 365")
		}
		cfg.RecentHistoryDays = days
	}
	if value := strings.TrimSpace(os.Getenv("VANI_CONTEXT_TOKEN_BUDGET")); value != "" {
		budget, err := strconv.Atoi(value)
		if err != nil || budget < 100 || budget > 10000 {
			return Config{}, fmt.Errorf("VANI_CONTEXT_TOKEN_BUDGET must be an integer from 100 to 10000")
		}
		cfg.ContextTokenBudget = budget
	}
	if value := strings.TrimSpace(os.Getenv("VANI_WORKER_INTERVAL_MS")); value != "" {
		ms, err := strconv.Atoi(value)
		if err != nil || ms < 100 || ms > 60000 {
			return Config{}, fmt.Errorf("VANI_WORKER_INTERVAL_MS must be an integer from 100 to 60000")
		}
		cfg.WorkerInterval = time.Duration(ms) * time.Millisecond
	}
	if value := strings.TrimSpace(os.Getenv("VANI_REFERENCE_TIME")); value != "" {
		reference, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return Config{}, fmt.Errorf("VANI_REFERENCE_TIME must be timezone-aware RFC3339: %w", err)
		}
		reference = reference.UTC()
		cfg.ReferenceTime = &reference
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
