// Package config loads server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Listen is the address the HTTP server (panel + API) binds to.
	Listen string
	// S3Listen is the address the S3-compatible API binds to ("" disables it).
	S3Listen string
	// S3Domain enables virtual-hosted-style requests (bucket.S3Domain).
	S3Domain string
	// DataDir holds the metadata database and object data.
	DataDir string
	// MasterKey (base64, 32 bytes) encrypts stored secrets. If empty, a key
	// file is generated in DataDir.
	MasterKey string
	// CookieSecure forces the Secure flag on session cookies. Enable it when
	// the panel is served over HTTPS (directly or behind a TLS proxy).
	CookieSecure bool
	// SessionTTL is how long a login session stays valid without activity.
	SessionTTL time.Duration
	// SetupToken, when set, must be supplied to create the first admin account.
	SetupToken string
	// MetricsToken enables /metrics, protected by this bearer token.
	MetricsToken string
	// AuditRetention is how long audit log entries are kept.
	AuditRetention time.Duration
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

func Load() (Config, error) {
	c := Config{
		Listen:       env("ACS_LISTEN", ":8080"),
		S3Listen:     env("ACS_S3_LISTEN", ":9000"),
		S3Domain:     os.Getenv("ACS_S3_DOMAIN"),
		DataDir:      env("ACS_DATA_DIR", "./data"),
		MasterKey:    os.Getenv("ACS_MASTER_KEY"),
		SetupToken:   os.Getenv("ACS_SETUP_TOKEN"),
		MetricsToken: os.Getenv("ACS_METRICS_TOKEN"),
		LogLevel:     env("ACS_LOG_LEVEL", "info"),
	}
	if c.S3Listen == "off" {
		c.S3Listen = ""
	}

	var err error
	if c.CookieSecure, err = strconv.ParseBool(env("ACS_COOKIE_SECURE", "false")); err != nil {
		return c, fmt.Errorf("ACS_COOKIE_SECURE: %w", err)
	}
	if c.SessionTTL, err = time.ParseDuration(env("ACS_SESSION_TTL", "168h")); err != nil {
		return c, fmt.Errorf("ACS_SESSION_TTL: %w", err)
	}
	if c.AuditRetention, err = time.ParseDuration(env("ACS_AUDIT_RETENTION", "2160h")); err != nil {
		return c, fmt.Errorf("ACS_AUDIT_RETENTION: %w", err)
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
