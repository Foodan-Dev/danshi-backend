// Command danshi-legacy-import imports and verifies the retired Python database snapshot.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/legacyimporter"
)

const (
	sourceEnv = "DANSHI_LEGACY_SOURCE_URL"
	targetEnv = "DATABASE_URL"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "danshi-legacy-import failed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	if len(args) != 1 || (args[0] != "import" && args[0] != "verify") {
		return fmt.Errorf("usage: danshi-legacy-import import|verify")
	}
	sourceDSN, targetDSN := os.Getenv(sourceEnv), os.Getenv(targetEnv)
	if sourceDSN == "" {
		return fmt.Errorf("missing environment variable %s", sourceEnv)
	}
	if targetDSN == "" {
		return fmt.Errorf("missing environment variable %s", targetEnv)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if args[0] == "import" {
		return legacyimporter.Import(ctx, sourceDSN, targetDSN, output)
	}
	return legacyimporter.Verify(ctx, sourceDSN, targetDSN, output)
}
