// Package hyperv is host-companion's sole PowerShell touchpoint, mirroring
// the rationale in internal/scvmm: no Go SDK exists for the Hyper-V module's
// cmdlets (Get-VM, Get-VMHardDiskDrive) or for live Get-Counter sampling, so
// we shell out narrowly to a local powershell.exe process per poll cycle.
// Unlike scvmm.Client, this always runs against the LOCAL host — no remote
// credential needed, since host-companion is deployed one-per-Hyper-V-host
// (see build-hyperv-vm-disk-map.ps1 / collect-hyperv-vm-disk.ps1, which this
// package ports).
package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildDiskMap runs Get-VM | Get-VMHardDiskDrive locally and returns a
// resolved DiskMap. Ports build-hyperv-vm-disk-map.ps1's cache-builder query;
// the caller is responsible for bounding this with a timeout via ctx (the
// original script backgrounds this with -TimeoutSec 300 because VMMS can be
// slow under load — same reasoning applies here).
func BuildDiskMap(ctx context.Context) (*DiskMap, error) {
	script := `Get-VM | ForEach-Object { $vm = $_; Get-VMHardDiskDrive -VM $vm -ErrorAction SilentlyContinue | Select-Object @{n='VMId';e={$vm.Id.ToString()}}, @{n='VMName';e={$vm.Name}}, Path } | ConvertTo-Json -Depth 3`

	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("powershell (disk map): %w: %s", err, stderr.String())
	}

	entries, err := decodeMaybeArray[VMDiskEntry](stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("decoding disk map: %w", err)
	}

	m := &DiskMap{ByPath: map[string]VMDiskEntry{}, ByID: map[string]VMDiskEntry{}}
	for _, e := range entries {
		if e.Path != "" {
			m.ByPath[normalizePath(e.Path)] = e
		}
		if e.VMId != "" {
			m.ByID[strings.ToLower(e.VMId)] = e
		}
	}
	return m, nil
}

func normalizePath(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

// decodeMaybeArray handles ConvertTo-Json's quirk of emitting a bare object
// (not wrapped in []) when the source collection has exactly one element —
// same helper as internal/scvmm/client.go.
func decodeMaybeArray[T any](data []byte) ([]T, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var out []T
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return nil, fmt.Errorf("decoding array: %w", err)
		}
		return out, nil
	}
	var single T
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return nil, fmt.Errorf("decoding single object: %w", err)
	}
	return []T{single}, nil
}
