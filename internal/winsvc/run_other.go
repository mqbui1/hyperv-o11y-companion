//go:build !windows

package winsvc

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// run is the non-Windows dev/test path: runs work in the foreground until
// SIGINT/SIGTERM. Production always builds/runs on Windows (run_windows.go).
func run(name string, work func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return work(ctx)
}
