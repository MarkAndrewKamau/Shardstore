package config

import (
	"flag"
	"log/slog"
	"testing"
)

func parse(t *testing.T, args ...string) (*Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(testWriter{t})
	return Parse(fs, args)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func TestParseDefaults(t *testing.T) {
	cfg, err := parse(t)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != ":9000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9000")
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "data")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.LogJSON {
		t.Error("LogJSON = true, want false")
	}
	if cfg.NodeID == "" {
		t.Error("NodeID is empty, want hostname-derived default")
	}
}

func TestParseFlags(t *testing.T) {
	cfg, err := parse(t,
		"-node-id", "n1",
		"-addr", ":9100",
		"-data-dir", "/tmp/shardstore-test",
		"-log-level", "debug",
		"-log-json",
	)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.NodeID != "n1" {
		t.Errorf("NodeID = %q, want %q", cfg.NodeID, "n1")
	}
	if cfg.ListenAddr != ":9100" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9100")
	}
	if cfg.DataDir != "/tmp/shardstore-test" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/tmp/shardstore-test")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
	if !cfg.LogJSON {
		t.Error("LogJSON = false, want true")
	}
}

func TestParseInvalidLogLevel(t *testing.T) {
	_, err := parse(t, "-log-level", "loud")
	if err == nil {
		t.Fatal("Parse with invalid log level: want error, got nil")
	}
}

func TestParseEmptyNodeID(t *testing.T) {
	t.Setenv("SHARDSTORE_NODE_ID", "")
	_, err := parse(t, "-node-id", "")
	if err == nil {
		t.Fatal("Parse with empty node id: want error, got nil")
	}
}

func TestEnvOverridesDefaults(t *testing.T) {
	t.Setenv("SHARDSTORE_ADDR", ":9800")
	t.Setenv("SHARDSTORE_LOG_LEVEL", "error")
	t.Setenv("SHARDSTORE_LOG_FORMAT", "json")
	cfg, err := parse(t)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != ":9800" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9800")
	}
	if cfg.LogLevel != slog.LevelError {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelError)
	}
	if !cfg.LogJSON {
		t.Error("LogJSON = false, want true (from SHARDSTORE_LOG_FORMAT=json)")
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	t.Setenv("SHARDSTORE_ADDR", ":9800")
	cfg, err := parse(t, "-addr", ":9400")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != ":9400" {
		t.Errorf("ListenAddr = %q, want %q (flag beats env)", cfg.ListenAddr, ":9400")
	}
}
