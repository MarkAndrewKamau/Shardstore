// Package config loads shardstore node configuration from environment
// variables (SHARDSTORE_*) and command-line flags. Flags override env,
// env overrides defaults.
package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
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
		NodeID:     hostname,
		ListenAddr: ":9000",
		DataDir:    "data",
		LogLevel:   slog.LevelInfo,
		levelName:  "info",
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