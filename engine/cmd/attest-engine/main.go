package main

import (
	"fmt"
	"os"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("attest-engine %s\n", version)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "Usage: attest-engine <command>")
	fmt.Fprintln(os.Stderr, "Commands: version")
	os.Exit(1)
}
