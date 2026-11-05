package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/attest-framework/attest/engine/internal/buildinfo"
	"github.com/attest-framework/attest/engine/internal/server"
)

func main() {
	if dispatch(os.Args[1:]) {
		return
	}

	// Parse flags
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	debug := flag.Bool("debug", false, "enable debug logging (shorthand for --log-level=debug)")
	flag.Usage = func() {
		printUsage(os.Stderr)
		fmt.Fprintln(os.Stderr, "\nserver flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	// --debug overrides --log-level
	if *debug {
		*logLevel = "debug"
	}

	// Configure logger
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "invalid log level: %s\n", *logLevel)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Create server
	srv := server.New(os.Stdin, os.Stdout, logger)
	if err := server.RegisterBuiltinHandlers(srv); err != nil {
		logger.Error("engine startup configuration error", "err", err)
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	// Handle signals
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("engine starting", "version", buildinfo.Version)
	if err := srv.Run(ctx); err != nil {
		logger.Error("engine error", "err", err)
		os.Exit(1)
	}
	logger.Info("engine shutdown complete")
}
