// Package config loads the scvmm-poller / host-companion YAML config files.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type ScvmmConfig struct {
	Server         string `yaml:"server"`
	Cluster        string `yaml:"cluster"`
	CredentialName string `yaml:"credential_name"`
}

type SplunkConfig struct {
	Realm          string `yaml:"realm"`
	CredentialName string `yaml:"credential_name"`
}

type OTLPConfig struct {
	Endpoint string `yaml:"endpoint"`
	Insecure bool   `yaml:"insecure"`
}

type MetricsConfig struct {
	Interval                time.Duration `yaml:"interval"`
	IncludeVMStorageNetwork bool          `yaml:"include_vm_storage_network"`
	IncludeVMVhd            bool          `yaml:"include_vm_vhd"`
	IncludeVMPerf           bool          `yaml:"include_vm_perf"`
}

type GuestOSConfig struct {
	Interval           time.Duration `yaml:"interval"`
	SecureBootFallback bool          `yaml:"secure_boot_fallback"`
	NameHeuristic      bool          `yaml:"name_heuristic"`
	LinuxNameMarkers   []string      `yaml:"linux_name_markers"`
}

// PollerConfig is cmd/scvmm-poller's config schema (config/scvmm-poller.yaml).
type PollerConfig struct {
	Scvmm       ScvmmConfig   `yaml:"scvmm"`
	Splunk      SplunkConfig  `yaml:"splunk"`
	OTLP        OTLPConfig    `yaml:"otlp"`
	Metrics     MetricsConfig `yaml:"metrics"`
	GuestOS     GuestOSConfig `yaml:"guest_os"`
	HostInclude []string      `yaml:"host_include"`
}

func LoadPoller(path string) (*PollerConfig, error) {
	var c PollerConfig
	if err := loadYAML(path, &c); err != nil {
		return nil, err
	}
	if c.Scvmm.Server == "" {
		return nil, fmt.Errorf("scvmm.server is required")
	}
	if c.Metrics.Interval == 0 {
		c.Metrics.Interval = 60 * time.Second
	}
	if c.GuestOS.Interval == 0 {
		c.GuestOS.Interval = time.Hour
	}
	return &c, nil
}

type DiskMapConfig struct {
	BuildInterval time.Duration `yaml:"build_interval"`
	BuildTimeout  time.Duration `yaml:"build_timeout"`
}

type DiskMetricsConfig struct {
	SampleInterval time.Duration `yaml:"sample_interval"`
	LatencyScale   float64       `yaml:"latency_scale"`
}

// GuestProbeConfig configures Tier 1.5 (PowerShell Direct guest probe, see
// docs/phase3-guest-probe-plan.md). Disabled by default — enabling this in
// production is gated on the go/no-go criteria in that doc, not just a
// config flag flip.
type GuestProbeConfig struct {
	Enabled bool `yaml:"enabled"`
	// VMInclude is an opt-in curated subset by VM name (filepath.Match
	// glob patterns, e.g. "WebServer*"; matched in
	// cmd/host-companion/main.go). Empty means no VMs are probed even if
	// Enabled is true — there is no fleet-wide default; Tier 1.5 starts
	// opt-in until go/no-go criterion #1 (fleet-scale Invoke-Command
	// -VMId latency) is validated.
	VMInclude      []string      `yaml:"vm_include"`
	SampleInterval time.Duration `yaml:"sample_interval"`
	SampleTimeout  time.Duration `yaml:"sample_timeout"`
	CredentialName string        `yaml:"credential_name"`
}

// HostCompanionConfig is cmd/host-companion's config schema
// (config/host-companion.yaml).
type HostCompanionConfig struct {
	OTLP        OTLPConfig        `yaml:"otlp"`
	DiskMap     DiskMapConfig     `yaml:"disk_map"`
	DiskMetrics DiskMetricsConfig `yaml:"disk_metrics"`
	GuestProbe  GuestProbeConfig  `yaml:"guest_probe"`
}

func LoadHostCompanion(path string) (*HostCompanionConfig, error) {
	var c HostCompanionConfig
	if err := loadYAML(path, &c); err != nil {
		return nil, err
	}
	if c.DiskMap.BuildInterval == 0 {
		c.DiskMap.BuildInterval = time.Hour
	}
	if c.DiskMap.BuildTimeout == 0 {
		c.DiskMap.BuildTimeout = 300 * time.Second
	}
	if c.DiskMetrics.SampleInterval == 0 {
		c.DiskMetrics.SampleInterval = 60 * time.Second
	}
	if c.DiskMetrics.LatencyScale == 0 {
		c.DiskMetrics.LatencyScale = 1000.0
	}
	if c.GuestProbe.SampleInterval == 0 {
		c.GuestProbe.SampleInterval = 5 * time.Minute
	}
	if c.GuestProbe.SampleTimeout == 0 {
		c.GuestProbe.SampleTimeout = 30 * time.Second
	}
	if c.GuestProbe.CredentialName == "" {
		c.GuestProbe.CredentialName = "hyperv-o11y/guest-probe"
	}
	return &c, nil
}

func loadYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parsing config %s: %w", path, err)
	}
	return nil
}
