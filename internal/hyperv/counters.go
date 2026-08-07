package hyperv

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// SampleStorageCounters runs a single live Get-Counter collection against
// the Hyper-V Virtual Storage Device object's Latency/Read Bytes per
// sec/Write Bytes per sec counters, mirroring collect-hyperv-vm-disk.ps1's
// per-cycle sampling (that script also samples via Get-Counter directly,
// not the windowsperfcounters OTel receiver, so per-instance VM resolution
// can happen before export).
func SampleStorageCounters(ctx context.Context) ([]StorageSample, error) {
	script := `
$paths = '\Hyper-V Virtual Storage Device(*)\Latency','\Hyper-V Virtual Storage Device(*)\Read Bytes/sec','\Hyper-V Virtual Storage Device(*)\Write Bytes/sec'
Get-Counter -Counter $paths -ErrorAction SilentlyContinue |
  Select-Object -ExpandProperty CounterSamples |
  Select-Object Path, InstanceName, CookedValue |
  ConvertTo-Json -Depth 3
`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("powershell (Get-Counter): %w: %s", err, stderr.String())
	}
	return decodeMaybeArray[StorageSample](stdout.Bytes())
}
