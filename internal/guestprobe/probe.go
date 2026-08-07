// Package guestprobe implements Tier 1.5 — the PowerShell Direct guest
// probe (see docs/phase3-guest-probe-plan.md). Invoke-Command -VMId tunnels
// a PowerShell session into a guest VM over VMBus (Hyper-V's host<->guest
// integration-component channel): no guest network path, no guest-facing
// firewall rule, no in-guest agent process. Requires guest Integration
// Services running and a credential valid inside the guest. This is the
// only mechanism in this repo that reads data actually inside a guest OS,
// closing gaps #2 (static-memory VM pressure) and #4 (guest filesystem
// used %) without deploying anything inside the guest — the constraint
// that rules out Tier 2 for this customer.
package guestprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/rccl/hyperv-o11y-companion/internal/creds"
)

// FilesystemSample is one fixed volume's capacity, as seen from inside the
// guest via Get-Volume.
type FilesystemSample struct {
	DriveLetter   string  `json:"DriveLetter"`
	Size          float64 `json:"Size"`
	SizeRemaining float64 `json:"SizeRemaining"`
}

// UsedPercent returns the used-space percentage for this volume, or false
// if Size is zero (unusable sample — skip rather than divide by zero).
func (s FilesystemSample) UsedPercent() (float64, bool) {
	if s.Size <= 0 {
		return 0, false
	}
	used := s.Size - s.SizeRemaining
	return used / s.Size * 100, true
}

// script is invoked as `& { param($VMId, $User, $PassEnvName) ... }
// $VMId $User $PassEnvName` — the password is passed via a process
// environment variable, not a command-line argument, so it never appears
// in a process listing (ps/Get-Process command line). Invoke-Command's own
// timeout is bounded by the caller's ctx; PowerShell Direct sessions can
// hang if guest Integration Services are missing/stale (see the go/no-go
// criteria in docs/phase3-guest-probe-plan.md), so callers MUST pass a
// per-VM timeout context, not an unbounded one.
const script = `& {
param($VMId, $User, $PassEnvName)
$pass = [Environment]::GetEnvironmentVariable($PassEnvName, 'Process')
$sec = ConvertTo-SecureString -String $pass -AsPlainText -Force
$cred = New-Object System.Management.Automation.PSCredential($User, $sec)
try {
    Invoke-Command -VMId $VMId -Credential $cred -ScriptBlock {
        Get-Volume | Where-Object { $_.DriveType -eq 'Fixed' -and $_.DriveLetter } |
            Select-Object DriveLetter, Size, SizeRemaining
    } | ConvertTo-Json -Depth 3
} finally {
    Remove-Variable pass, sec, cred -ErrorAction SilentlyContinue
}
}`

const passEnvName = "HYPERV_O11Y_GUESTPROBE_PASS"

// SampleFilesystem queries fixed-volume capacity inside the guest VM
// identified by vmID, over VMBus via PowerShell Direct. ctx should carry a
// per-VM timeout (see docs/phase3-guest-probe-plan.md go/no-go criterion
// #1 — Invoke-Command -VMId is not free at fleet scale).
func SampleFilesystem(ctx context.Context, vmID string, cred creds.Credential) ([]FilesystemSample, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script, vmID, cred.Username, passEnvName)
	cmd.Env = append(os.Environ(), passEnvName+"="+cred.Password)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("powershell (Invoke-Command -VMId %s): %w: %s", vmID, err, stderr.String())
	}
	return decodeMaybeArray[FilesystemSample](stdout.Bytes())
}

// decodeMaybeArray handles ConvertTo-Json's quirk of emitting a bare object
// (not wrapped in []) when the source collection has exactly one element —
// same helper as internal/hyperv and internal/scvmm.
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
