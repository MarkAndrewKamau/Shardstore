// Package config loads shardstore node configuration from environment
// variables (SHARDSTORE_*) and command-line flags. Flags override env,
// env overrides defaults.
package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration for a shardstore node.
type Config struct {
	// NodeID uniquely identifies this node in the cluster.
	NodeID string
	// ListenAddr is the address the API server binds to.
	ListenAddr string
	// DataDir is the root directory for local shard storage.
	DataDir string
	// LogLevel controls the verbosity of structured logging.
	LogLevel slog.Level
	// LogJSON switches slog output between JSON and text.
	LogJSON bool

	// ECDataShards is the number of data shards (k) for erasure coding.
	ECDataShards int
	// ECParityShards is the number of parity shards (m) for erasure coding.
	ECParityShards int
	// StripeSize is the size of each EC stripe in bytes.
	StripeSize int64
	// PermissiveAuth disables SigV4 verification for local development.
	PermissiveAuth bool

	// levelName is the string form of LogLevel, used for flag/env parsing.
	levelName string
}

// Default returns a Config populated from environment variables
// (SHARDSTORE_*) with sensible fallbacks.
func Default() Config {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "node"
	}
	cfg := Config{
		NodeID:         hostname,
		ListenAddr:     ":9000",
		DataDir:        "data",
		LogLevel:       slog.LevelInfo,
		levelName:      "info",
		ECDataShards:   4,
		ECParityShards: 2,
		StripeSize:     8 << 20,
		PermissiveAuth: false,
	}
	cfg.applyEnv()
	return cfg
}

// RegisterFlags registers shardstore's configuration flags on fs.
// Subcommands may then hand fs and their args to Parse.
func RegisterFlags(fs *flag.FlagSet, cfg *Config) {
	fs.StringVar(&cfg.NodeID, "node-id", cfg.NodeID, "unique node identifier (env SHARDSTORE_NODE_ID)")
	fs.StringVar(&cfg.ListenAddr, "addr", cfg.ListenAddr, "API listen address (env SHARDSTORE_ADDR)")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "root directory for shard storage (env SHARDSTORE_DATA_DIR)")
	fs.StringVar(&cfg.levelName, "log-level", cfg.levelName, "log level: debug|info|warn|error (env SHARDSTORE_LOG_LEVEL)")
	fs.BoolVar(&cfg.LogJSON, "log-json", cfg.LogJSON, "log in JSON format (env SHARDSTORE_LOG_FORMAT=json)")
	fs.IntVar(&cfg.ECDataShards, "ec-data-shards", cfg.ECDataShards, "number of data shards (k) for erasure coding (env SHARDSTORE_EC_DATA_SHARDS)")
	fs.IntVar(&cfg.ECParityShards, "ec-parity-shards", cfg.ECParityShards, "number of parity shards (m) for erasure coding (env SHARDSTORE_EC_PARITY_SHARDS)")
	fs.Int64Var(&cfg.StripeSize, "stripe-size", cfg.StripeSize, "stripe size in bytes (env SHARDSTORE_STRIPE_SIZE)")
	fs.BoolVar(&cfg.PermissiveAuth, "permissive-auth", cfg.PermissiveAuth, "disable SigV4 verification for local dev (env SHARDSTORE_PERMISSIVE_AUTH)")
}

// Parse applies flags on top of env-derived defaults and validates the result.
func Parse(fs *flag.FlagSet, args []string) (*Config, error) {
	cfg := Default()
	RegisterFlags(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	lv, err := parseLevel(cfg.levelName)
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = lv
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// validate checks cross-field invariants.
func (c *Config) validate() error {
	if c.NodeID == "" {
		return fmt.Errorf("node-id must not be empty")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data-dir must not be empty")
	}
	if c.ECDataShards < 1 {
		return fmt.Errorf("ec-data-shards must be >= 1, got %d", c.ECDataShards)
	}
	if c.ECParityShards < 1 {
		return fmt.Errorf("ec-parity-shards must be >= 1, got %d", c.ECParityShards)
	}
	if c.ECDataShards+c.ECParityShards > 256 {
		return fmt.Errorf("total shards %d exceeds the 256-shard limit of Reed-Solomon", c.ECDataShards+c.ECParityShards)
	}
	if c.StripeSize <= 0 {
		return fmt.Errorf("stripe-size must be > 0, got %d", c.StripeSize)
	}
	return nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("SHARDSTORE_NODE_ID"); v != "" {
		c.NodeID = v
	}
	if v := os.Getenv("SHARDSTORE_ADDR"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("SHARDSTORE_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("SHARDSTORE_LOG_LEVEL"); v != "" {
		if lv, err := parseLevel(v); err == nil {
			c.LogLevel = lv
			c.levelName = v
		}
	}
	if v := os.Getenv("SHARDSTORE_LOG_FORMAT"); v == "json" {
		c.LogJSON = true
	}
	if v := os.Getenv("SHARDSTORE_EC_DATA_SHARDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ECDataShards = n
		}
	}
	if v := os.Getenv("SHARDSTORE_EC_PARITY_SHARDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ECParityShards = n
		}
	}
	if v := os.Getenv("SHARDSTORE_STRIPE_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.StripeSize = n
		}
	}
	if v := os.Getenv("SHARDSTORE_PERMISSIVE_AUTH"); v == "true" || v == "1" {
		c.PermissiveAuth = true
	}
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want debug|info|warn|error)", s)
	}
}
