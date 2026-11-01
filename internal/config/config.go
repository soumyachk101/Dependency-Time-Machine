// Package config loads and validates the server's environment
// configuration. Every var has a documented default; validation is
// strict enough to fail fast on operator mistakes but never requires a
// secret to boot (least-privilege default: features are off until their
// secret is explicitly supplied).
package config

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ensembyte/dtm/internal/logx"
)

// Data-driver names, re-exported for callers that need to branch on
// driver without re-importing the store package.
const (
	SQLiteDriver   = "sqlite"
	PostgresDriver = "postgres"
	MemoryDriver   = "memory"
)

// Config is the validated, server-level configuration.
type Config struct {
	DataDriver    string // sqlite | postgres | memory
	SQLitePath    string
	DatabaseURL   string
	HTTPAddr      string
	LogLevel      string
	StaticDir     string

	RateLimitRPS   float64
	RateLimitBurst int

	RunnerMode     string
	RunnerImage    string
	RunnerCPULimit string
	RunnerMemLimit string
	RunnerDiskLimit string
	RunnerWallLimit time.Duration

	OSVEndpoint   string
	OnlineAdv     bool
	RetentionDays int

	// Secrets — pointer nil means "feature disabled".
	WebhookSecret *string
	GitHubAppID   *string
	GitHubKeyFile *string
}

// Load reads the environment (via GetEnv) and applies defaults +
// validation. It returns an error on any invalid value so the process
// fails fast at boot rather than at request time.
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{}

	c.DataDriver = def(getenv("DTM_DATA_DRIVER"), "sqlite")
	c.SQLitePath = def(getenv("DTM_SQLITE_PATH"), "./data/dtm.sqlite")
	c.DatabaseURL = getenv("DTM_DATABASE_URL")
	c.HTTPAddr = def(getenv("DTM_HTTP_ADDR"), "127.0.0.1:8080")
	c.LogLevel = def(getenv("DTM_LOG_LEVEL"), logx.Info)
	c.StaticDir = getenv("DTM_STATIC_DIR")

	rps, err := parseFloatDef(getenv("DTM_RATE_LIMIT_RPS"), 10)
	if err != nil {
		return nil, err
	}
	c.RateLimitRPS = rps
	burst, err := parseIntDef(getenv("DTM_RATE_LIMIT_BURST"), 20)
	if err != nil {
		return nil, err
	}
	c.RateLimitBurst = burst

	c.RunnerMode = def(getenv("DTM_RUNNER_MODE"), "auto")
	c.RunnerImage = def(getenv("DTM_DOCKER_IMAGE"), "ghcr.io/ensembyte/dtm-runner:1.0.0")
	c.RunnerCPULimit = def(getenv("DTM_RUNNER_CPU_LIMIT"), "2")
	c.RunnerMemLimit = def(getenv("DTM_RUNNER_MEMORY_LIMIT"), "4g")
	c.RunnerDiskLimit = def(getenv("DTM_RUNNER_DISK_LIMIT"), "10g")
	c.RunnerWallLimit, err = parseDurDef(getenv("DTM_RUNNER_WALL_CLOCK_LIMIT"), 30*time.Minute)
	if err != nil {
		return nil, err
	}

	c.OSVEndpoint = getenv("DTM_OSV_ENDPOINT")
	c.OnlineAdv = parseBoolDef(getenv("DTM_ENABLE_ONLINE_ADVISORIES"), true)
	c.RetentionDays, err = parseIntDef(getenv("DTM_ARTIFACT_RETENTION_DAYS"), 14)
	if err != nil {
		return nil, err
	}

	if v := getenv("DTM_GITHUB_WEBHOOK_SECRET"); v != "" {
		c.WebhookSecret = &v
	}
	if v := getenv("DTM_GITHUB_APP_ID"); v != "" {
		c.GitHubAppID = &v
	}
	if v := getenv("DTM_GITHUB_APP_PRIVATE_KEY_FILE"); v != "" {
		c.GitHubKeyFile = &v
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	switch c.DataDriver {
	case "sqlite", "postgres", "memory":
	default:
		return fmt.Errorf("config: DTM_DATA_DRIVER must be sqlite|postgres|memory, got %q", c.DataDriver)
	}
	switch c.RunnerMode {
	case "auto", "container", "degraded":
	default:
		return fmt.Errorf("config: DTM_RUNNER_MODE must be auto|container|degraded, got %q", c.RunnerMode)
	}
	if c.RateLimitBurst < 1 {
		return fmt.Errorf("config: DTM_RATE_LIMIT_BURST must be >= 1")
	}
	if c.RateLimitRPS < 0 {
		return fmt.Errorf("config: DTM_RATE_LIMIT_RPS must be >= 0")
	}
	if c.HTTPAddr == "" {
		return fmt.Errorf("config: DTM_HTTP_ADDR must be non-empty")
	}
	return nil
}

// ---- env accessors + typed parsers ----------------------------------------

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func parseFloatDef(v string, d float64) (float64, error) {
	if v == "" {
		return d, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: invalid float %q: %w", v, err)
	}
	return f, nil
}

func parseIntDef(v string, d int) (int, error) {
	if v == "" {
		return d, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid int %q: %w", v, err)
	}
	return n, nil
}

func parseDurDef(v string, d time.Duration) (time.Duration, error) {
	if v == "" {
		return d, nil
	}
	dur, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid duration %q: %w", v, err)
	}
	return dur, nil
}

func parseBoolDef(v string, d bool) bool {
	if v == "" {
		return d
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return d
}
