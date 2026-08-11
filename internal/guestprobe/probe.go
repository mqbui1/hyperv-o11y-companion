// Package guestprobe implements Tier 1.5 — the PowerShell Direct guest
// probe (see docs/phase3-guest-probe-plan.md). Invoke-Command -VMId tunnels
// a PowerShell session into a guest VM over VMBus (Hyper-V's host<->guest
// integration-component channel): no guest network path, no guest-facing
// firewall rule, no in-guest agent process. Requires guest Integration
// Services running and a credential valid inside the guest. This is the
// only mechanism in this repo that reads data actually inside a guest OS,
// closing gaps #2 (static-memory VM pressure) and #4 (guest filesystem
// used %) without deploying anything inside the guest — this customer has
// ruled out deploying any collector inside guest VMs, opt-in or otherwise.
package guestprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/splunk/hyperv-o11y-companion/internal/creds"
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

// MemorySample is the guest's own OS-reported physical memory usage
// (Win32_OperatingSystem, values natively in KB). Unlike Hyper-V's
// "Hyper-V Dynamic Memory VM" Current Pressure counter (Tier 1), this
// works for static-memory VMs too — it's the guest asking itself how much
// memory it's using, not a Dynamic Memory balloon-driver artifact — which
// is what closes gap #2 for static-memory VMs specifically.
type MemorySample struct {
	TotalVisibleMemoryKB float64 `json:"TotalVisibleMemoryKB"`
	FreePhysicalMemoryKB float64 `json:"FreePhysicalMemoryKB"`
}

// UsedPercent returns the used-memory percentage, or false if
// TotalVisibleMemoryKB is zero.
func (m MemorySample) UsedPercent() (float64, bool) {
	if m.TotalVisibleMemoryKB <= 0 {
		return 0, false
	}
	used := m.TotalVisibleMemoryKB - m.FreePhysicalMemoryKB
	return used / m.TotalVisibleMemoryKB * 100, true
}

// GuestSample is one PowerShell Direct round trip's combined guest read.
// Filesystem (gap #4) and Memory (gap #2) are gathered in a SINGLE
// Invoke-Command session per VM per sample cycle, not two — the session
// itself is the expensive/risky part at fleet scale (go/no-go criterion
// #1 in docs/phase3-guest-probe-plan.md), not what's queried once inside
// one, so bundling every in-guest fact this repo needs into one round trip
// is the right default shape going forward.
type GuestSample struct {
	Filesystem []FilesystemSample `json:"Filesystem"`
	Memory     MemorySample       `json:"Memory"`
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
        $fs = Get-Volume | Where-Object { $_.DriveType -eq 'Fixed' -and $_.DriveLetter } |
            Select-Object DriveLetter, Size, SizeRemaining
        $os = Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize, FreePhysicalMemory
        [PSCustomObject]@{
            Filesystem = @($fs)
            Memory     = [PSCustomObject]@{
                TotalVisibleMemoryKB = $os.TotalVisibleMemorySize
                FreePhysicalMemoryKB = $os.FreePhysicalMemory
            }
        }
    } | ConvertTo-Json -Depth 4
} finally {
    Remove-Variable pass, sec, cred -ErrorAction SilentlyContinue
}
}`

const passEnvName = "HYPERV_O11Y_GUESTPROBE_PASS"

// Sample queries fixed-volume capacity (gap #4) and OS-reported memory
// usage (gap #2) inside the guest VM identified by vmID, in one
// PowerShell Direct session over VMBus. ctx should carry a per-VM timeout
// (see docs/phase3-guest-probe-plan.md go/no-go criterion #1 —
// Invoke-Command -VMId is not free at fleet scale).
func Sample(ctx context.Context, vmID string, cred creds.Credential) (*GuestSample, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script, vmID, cred.Username, passEnvName)
	cmd.Env = append(os.Environ(), passEnvName+"="+cred.Password)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("powershell (Invoke-Command -VMId %s): %w: %s", vmID, err, stderr.String())
	}
	var out GuestSample
	trimmed := bytes.TrimSpace(stdout.Bytes())
	if len(trimmed) == 0 {
		return &out, nil
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fmt.Errorf("decoding guest sample: %w", err)
	}
	return &out, nil
}
