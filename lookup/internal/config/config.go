package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr       string
	DBPath         string
	ServiceToken   string
	CandidateLimit int
	ReferenceTime  *time.Time
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:       envOr("VANI_LOOKUP_HTTP_ADDR", "127.0.0.1:18081"),
		DBPath:         envOr("VANI_DB_PATH", "../Context/data/vani.sqlite"),
		CandidateLimit: 500,
	}
	cfg.ServiceToken = strings.TrimSpace(os.Getenv("VANI_SERVICE_TOKEN"))
	if len(cfg.ServiceToken) < 16 {
		return Config{}, errors.New("VANI_SERVICE_TOKEN must contain at least 16 characters")
	}
	if value := strings.TrimSpace(os.Getenv("VANI_LOOKUP_CANDIDATE_LIMIT")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 10 || limit > 5000 {
			return Config{}, errors.New("VANI_LOOKUP_CANDIDATE_LIMIT must be from 10 to 5000")
		}
		cfg.CandidateLimit = limit
	}
	if value := strings.TrimSpace(os.Getenv("VANI_REFERENCE_TIME")); value != "" {
		reference, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return Config{}, fmt.Errorf("VANI_REFERENCE_TIME must be timezone-aware RFC3339: %w", err)
		}
		reference = reference.UTC()
		cfg.ReferenceTime = &reference
	}
	absolute, err := filepath.Abs(cfg.DBPath)
	if err != nil {
		return Config{}, fmt.Errorf("resolve VANI_DB_PATH: %w", err)
	}
	cfg.DBPath = absolute
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
