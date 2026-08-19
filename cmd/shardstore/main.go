// Command shardstore is a node in the Shardstore distributed object store.
//
// Subcommands:
//
//	server   run a storage node (API + future data path)
//	version  print build information
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"shardstore/internal/api"
	"shardstore/internal/config"
	"shardstore/internal/logging"
	"shardstore/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "shardstore: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "server":
		return runServer(args[1:])
	case "version":
		fmt.Println(version.String())
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `shardstore - distributed object store with erasure coding

Usage:
  shardstore server [flags]   run a storage node
  shardstore version          print build information

Run "shardstore server -h" for server flags.
`)
}

func runServer(args []string) error {
	cfg, err := config.Parse(flag.NewFlagSet("server", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	logger := logging.New(cfg.LogLevel, cfg.LogJSON)
	logger.Info("starting shardstore",
		"node_id", cfg.NodeID,
		"addr", cfg.ListenAddr,
		"data_dir", cfg.DataDir,
		"version", version.String(),
	)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.New(cfg.NodeID, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.ListenAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("api server: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			return err
		}
		logger.Info("shutdown complete")
	}
	return nil
}