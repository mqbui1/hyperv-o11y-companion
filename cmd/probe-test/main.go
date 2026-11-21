// cmd/probe-test/main.go — hardware validation harness for Phase 2 (2h/2f).
//
// Usage: probe-test.exe <vhdx-path> [<vhdx-path>...]
//
// Calls guestfs.FilesystemUsedPercent for each provided VHDX path and prints
// whether it succeeded (OK) or was skipped (SKIP) with the classified reason.
// Exit 0 if every path returned OK; exit 1 if any path was skipped or errored.
package main

import (
	"fmt"
	"os"

	"github.com/splunk/hyperv-o11y-companion/internal/guestfs"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe-test.exe <vhdx-path> [...]")
		os.Exit(1)
	}
	exitCode := 0
	for _, path := range os.Args[1:] {
		pct, err := guestfs.FilesystemUsedPercent(path)
		if err != nil {
			reason := guestfs.ClassifySkipReason(err)
			fmt.Printf("SKIP  %s\n      skip_reason=%s\n      err=%v\n", path, reason, err)
			exitCode = 1
		} else {
			fmt.Printf("OK    %s\n      used=%.2f%%\n", path, pct)
		}
	}
	os.Exit(exitCode)
}
