// Package scvmm is the sole PowerShell touchpoint in this service: SCVMM has
// no on-prem REST API and no Go SDK, so we shell out to a short-lived
// powershell.exe process per poll cycle to run the same
// VirtualMachineManager cmdlets the existing scripts use
// (Get-SCVMHost / Get-SCVirtualMachine), and parse their ConvertTo-Json
// output. Everything downstream (scheduling, retries, OTLP export,
// credentials) is native Go — see README.md "Why Go still shells out to
// PowerShell for SCVMM".
package scvmm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

type Client struct {
	Server         string
	Username       string
	Password       string
	Cluster        string
	Timeout        time.Duration
}

func New(server, username, password, cluster string) *Client {
	return &Client{Server: server, Username: username, Password: password, Cluster: cluster, Timeout: 90 * time.Second}
}

// run executes a PowerShell script that connects to SCVMM with the configured
// credential and prints ConvertTo-Json output on stdout. The credential is
// passed via a temporary PSCredential built from SecureString in-process
// (never written to disk, never placed in argv/process listing).
func (c *Client) run(ctx context.Context, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	prelude := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
Import-Module VirtualMachineManager -ErrorAction SilentlyContinue -WarningAction SilentlyContinue 2>$null
$pw = ConvertTo-SecureString $env:HYPERV_O11Y_SCVMM_PW -AsPlainText -Force
$cred = New-Object System.Management.Automation.PSCredential($env:HYPERV_O11Y_SCVMM_USER, $pw)
$vmm = Get-SCVMMServer -ComputerName $env:HYPERV_O11Y_SCVMM_SERVER -Credential $cred -ErrorAction Stop
`)
	full := prelude + script

	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", full)
	cmd.Env = append(cmd.Env,
		"HYPERV_O11Y_SCVMM_USER="+c.Username,
		"HYPERV_O11Y_SCVMM_PW="+c.Password,
		"HYPERV_O11Y_SCVMM_SERVER="+c.Server,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("powershell: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Hosts returns Get-SCVMHost results, optionally scoped to c.Cluster.
func (c *Client) Hosts(ctx context.Context) ([]Host, error) {
	clusterFilter := ""
	if c.Cluster != "" {
		clusterFilter = fmt.Sprintf(`| Where-Object { $_.HostCluster -and (($_.HostCluster.Name -split '\.')[0] -eq %q) }`, c.Cluster)
	}
	script := fmt.Sprintf(`Get-SCVMHost -VMMServer $vmm %s | Select-Object Name,OverallState,CPUUsagePercent,TotalMemoryMB,AvailableMemoryMB,@{n='ClusterName';e={$_.HostCluster.Name}} | ConvertTo-Json -Depth 4`, clusterFilter)
	out, err := c.run(ctx, script)
	if err != nil {
		return nil, err
	}
	return decodeMaybeArray[Host](out)
}

// VMs returns Get-SCVirtualMachine results, optionally scoped to c.Cluster.
func (c *Client) VMs(ctx context.Context) ([]VM, error) {
	clusterFilter := ""
	if c.Cluster != "" {
		clusterFilter = fmt.Sprintf(`| Where-Object { $_.VMHost -and $_.VMHost.HostCluster -and (($_.VMHost.HostCluster.Name -split '\.')[0] -eq %q) }`, c.Cluster)
	}
	script := fmt.Sprintf(`Get-SCVirtualMachine -VMMServer $vmm %s | Select-Object ID,Name,Status,@{n='VMHostName';e={$_.VMHost.Name}},CPUCount,MemoryAssignedMB,MemoryDemandMB,OperatingSystem,Generation | ConvertTo-Json -Depth 4`, clusterFilter)
	out, err := c.run(ctx, script)
	if err != nil {
		return nil, err
	}
	return decodeMaybeArray[VM](out)
}

// decodeMaybeArray handles ConvertTo-Json's quirk of emitting a bare object
// (not wrapped in []) when the source collection has exactly one element.
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
