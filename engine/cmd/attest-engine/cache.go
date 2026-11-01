package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func init() {
	register(command{name: "cache", summary: "show or clear the engine cache (stats|clear)", run: handleCacheCommand})
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
