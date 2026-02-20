package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/attest-ai/attest/engine/internal/server"
)

const version = "0.4.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("attest-engine %s\n", version)
			os.Exit(0)
		case "cache":
			handleCacheCommand(os.Args[2:])
			return
		}
	}

	// Parse flags
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	debug := flag.Bool("debug", false, "enable debug logging (shorthand for --log-level=debug)")
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
	server.RegisterBuiltinHandlers(srv)

	// Handle signals
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("engine starting", "version", version)
	if err := srv.Run(ctx); err != nil {
		logger.Error("engine error", "err", err)
		os.Exit(1)
	}
	logger.Info("engine shutdown complete")
}

// cacheDir returns the cache directory from ATTEST_CACHE_DIR env or ~/.attest/cache/.
func cacheDir() string {
	if dir := os.Getenv("ATTEST_CACHE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(home, ".attest", "cache")
}

// handleCacheCommand handles: attest-engine cache stats | cache clear
func handleCacheCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: attest-engine cache <stats|clear>")
		os.Exit(1)
	}

	dir := cacheDir()

	switch args[0] {
	case "stats":
		info, err := os.Stat(dir)
		if os.IsNotExist(err) {
			fmt.Println("cache directory does not exist:", dir)
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "stat cache dir: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("cache dir: %s\n", dir)
		fmt.Printf("exists:    true\n")
		fmt.Printf("is_dir:    %v\n", info.IsDir())

		// Walk and sum sizes
		var totalBytes int64
		var fileCount int
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read cache dir: %v\n", err)
			os.Exit(1)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			fi, err := entry.Info()
			if err != nil {
				continue
			}
			totalBytes += fi.Size()
			fileCount++
		}
		fmt.Printf("files:     %d\n", fileCount)
		fmt.Printf("size_bytes: %d\n", totalBytes)

	case "clear":
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Println("cache directory does not exist:", dir)
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read cache dir: %v\n", err)
			os.Exit(1)
		}
		var deleted int
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(os.Stderr, "remove %s: %v\n", path, err)
				continue
			}
			deleted++
		}
		fmt.Printf("cleared %d file(s) from %s\n", deleted, dir)

	default:
		fmt.Fprintf(os.Stderr, "unknown cache command: %s\n", args[0])
		os.Exit(1)
	}
}
