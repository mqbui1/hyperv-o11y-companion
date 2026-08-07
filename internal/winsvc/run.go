// Package winsvc lets the same binary run either as a real Windows Service
// (installed by the MSI) or in the foreground (local dev/testing on any OS).
// Production always uses the Windows-Service code path; run.go here is the
// OS-agnostic entrypoint that dispatches to the right implementation.
package winsvc

import "context"

// Run blocks until ctx is cancelled (foreground/dev) or the Windows Service
// Control Manager requests a stop (production). work is called once at
// startup with a context that's cancelled on shutdown.
func Run(name string, work func(ctx context.Context) error) error {
	return run(name, work)
}
