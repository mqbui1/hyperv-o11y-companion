// host-companion is the Phase-2 consolidated replacement for
// build-hyperv-vm-disk-map.ps1 + collect-hyperv-vm-disk.ps1. Runs as a
// Windows Service on every physical Hyper-V host, alongside the existing
// Splunk OTel Collector. Two independent tickers, sharing one in-memory
// disk map instead of the JSON cache file the original scripts used to
// hand off between separate processes:
//
//   - disk map builder: rebuilds the VHD-path -> VM lookup periodically
//     (Get-VM | Get-VMHardDiskDrive), same as build-hyperv-vm-disk-map.ps1.
//   - disk metrics sampler: samples the Hyper-V Virtual Storage Device
//     Perfmon counters directly (Get-Counter), resolves each instance to a
//     VM via the disk map, and exports vm.disk.{latency,read_bytes_sec,
//     write_bytes_sec} via OTLP — same as collect-hyperv-vm-disk.ps1,
//     including its empirically-confirmed seconds->ms latency scale fix
//     (see docs/known-gaps-remediation.md gap #5).
//
// No SCVMM/Splunk credentials are needed here — this exports to the
// host-local Splunk OTel Collector over plain OTLP; the collector's own
// signalfx exporter handles upstream auth.
package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/splunk/hyperv-o11y-companion/internal/config"
	"github.com/splunk/hyperv-o11y-companion/internal/creds"
	"github.com/splunk/hyperv-o11y-companion/internal/diskattr"
	"github.com/splunk/hyperv-o11y-companion/internal/guestprobe"
	"github.com/splunk/hyperv-o11y-companion/internal/hyperv"
	"github.com/splunk/hyperv-o11y-companion/internal/metricsexport"
	"github.com/splunk/hyperv-o11y-companion/internal/winsvc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func main() {
	configPath := flag.String("config", `C:\Program Files\Splunk\HyperVO11yCompanion\config\host-companion.yaml`, "path to host-companion.yaml")
	flag.Parse()

	cfg, err := config.LoadHostCompanion(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	err = winsvc.Run("hyperv-host-companion", func(ctx context.Context) error {
		return runService(ctx, cfg)
	})
	if err != nil {
		log.Fatalf("service exited with error: %v", err)
	}
}

// diskMapState guards the shared in-memory disk map between the builder
// goroutine and the sampler goroutine. Replaces the atomic-temp-file +
// same-volume-Move pattern build-hyperv-vm-disk-map.ps1 used to hand off to
// a separate process — both now live in this one service.
type diskMapState struct {
	mu sync.RWMutex
	m  *hyperv.DiskMap
}

func (s *diskMapState) get() *hyperv.DiskMap {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m
}

func (s *diskMapState) set(m *hyperv.DiskMap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = m
}

func runService(ctx context.Context, cfg *config.HostCompanionConfig) error {
	exp, err := metricsexport.New(ctx, "hyperv-host-companion", cfg.OTLP.Endpoint, cfg.OTLP.Insecure)
	if err != nil {
		return err
	}
	defer exp.Shutdown(ctx)

	latencyGauge, err := exp.Meter.Float64Gauge("vm.disk.latency",
		metric.WithDescription("VM virtual disk IO latency (ms, scale-corrected from raw seconds — see gap #5)"),
		metric.WithUnit("ms"))
	if err != nil {
		return err
	}
	readGauge, err := exp.Meter.Float64Gauge("vm.disk.read_bytes_sec", metric.WithUnit("By/s"))
	if err != nil {
		return err
	}
	writeGauge, err := exp.Meter.Float64Gauge("vm.disk.write_bytes_sec", metric.WithUnit("By/s"))
	if err != nil {
		return err
	}
	guestFsUsedGauge, err := exp.Meter.Float64Gauge("vm.guest.filesystem.used_percent",
		metric.WithDescription("Guest filesystem used space (%), sampled inside the guest via PowerShell Direct — Tier 1.5, gap #4"),
		metric.WithUnit("%"))
	if err != nil {
		return err
	}
	guestMemUsedGauge, err := exp.Meter.Float64Gauge("vm.guest.memory.used_percent",
		metric.WithDescription("Guest OS-reported memory used (%), sampled inside the guest via PowerShell Direct — Tier 1.5, gap #2 (works for static-memory VMs, unlike Hyper-V's Dynamic Memory-only Current Pressure counter)"),
		metric.WithUnit("%"))
	if err != nil {
		return err
	}

	state := &diskMapState{}
	buildDiskMap(ctx, cfg, state) // populate once at startup before the first sample cycle

	buildTicker := time.NewTicker(cfg.DiskMap.BuildInterval)
	defer buildTicker.Stop()
	sampleTicker := time.NewTicker(cfg.DiskMetrics.SampleInterval)
	defer sampleTicker.Stop()

	var guestProbeTicker *time.Ticker
	if cfg.GuestProbe.Enabled && len(cfg.GuestProbe.VMInclude) > 0 {
		guestProbeTicker = time.NewTicker(cfg.GuestProbe.SampleInterval)
		defer guestProbeTicker.Stop()
	}
	guestProbeC := func() <-chan time.Time {
		if guestProbeTicker == nil {
			return nil // nil channel: this case is simply never selected, cleanly disabling the tier
		}
		return guestProbeTicker.C
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-buildTicker.C:
			buildDiskMap(ctx, cfg, state)
		case <-sampleTicker.C:
			sampleAndExport(ctx, cfg, state, latencyGauge, readGauge, writeGauge)
		case <-guestProbeC:
			sampleGuestProbe(ctx, cfg, state, guestFsUsedGauge, guestMemUsedGauge)
		}
	}
}

// buildDiskMap rebuilds the VHD-path -> VM lookup. On timeout or error, keeps
// the previous map in place rather than blocking or emitting unattributed
// metrics — same fallback behavior as build-hyperv-vm-disk-map.ps1's
// -TimeoutSec handling.
func buildDiskMap(ctx context.Context, cfg *config.HostCompanionConfig, state *diskMapState) {
	buildCtx, cancel := context.WithTimeout(ctx, cfg.DiskMap.BuildTimeout)
	defer cancel()

	m, err := hyperv.BuildDiskMap(buildCtx)
	if err != nil {
		log.Printf("disk map build failed, keeping previous map: %v", err)
		return
	}
	state.set(m)
	log.Printf("disk map rebuilt: %d path entries, %d VM ids", len(m.ByPath), len(m.ByID))
}

// sampleAndExport closes gap #3 (VHD attribution): samples the live
// Hyper-V Virtual Storage Device counters, resolves each instance to a VM
// via the current disk map, and exports per-VM disk metrics. Instances that
// don't resolve (DVD/ISO/pass-through disks) are counted and skipped, not
// guessed at — same deliberate residual gap documented in
// docs/known-gaps-remediation.md gap #3.
func sampleAndExport(ctx context.Context, cfg *config.HostCompanionConfig, state *diskMapState, latencyGauge, readGauge, writeGauge metric.Float64Gauge) {
	m := state.get()
	if m == nil {
		log.Printf("disk metrics sample skipped: disk map not yet built")
		return
	}

	samples, err := hyperv.SampleStorageCounters(ctx)
	if err != nil {
		log.Printf("disk counter sample failed: %v", err)
		return
	}

	matched, unmatched := 0, 0
	for _, s := range samples {
		instance := s.InstanceName
		if instance == "" {
			instance = s.Path
		}
		vm, ok := diskattr.Resolve(m, instance)
		if !ok {
			unmatched++
			continue
		}
		matched++
		attrs := metric.WithAttributes(attribute.String("vm.name", vm.VMName))

		path := strings.ToLower(s.Path)
		switch {
		case strings.Contains(path, "latency"):
			latencyGauge.Record(ctx, s.CookedValue*cfg.DiskMetrics.LatencyScale, attrs)
		case strings.Contains(path, "read bytes"):
			readGauge.Record(ctx, s.CookedValue, attrs)
		case strings.Contains(path, "write bytes"):
			writeGauge.Record(ctx, s.CookedValue, attrs)
		}
	}
	log.Printf("disk metrics: matched=%d unmatched=%d (%.0f%% match rate)", matched, unmatched, matchRate(matched, unmatched))
}

func matchRate(matched, unmatched int) float64 {
	total := matched + unmatched
	if total == 0 {
		return 0
	}
	return float64(matched) / float64(total) * 100
}

// sampleGuestProbe closes gap #4 (guest filesystem used %) and gap #2
// (memory pressure, including static-memory VMs) via Tier 1.5: for each VM
// in the disk map matching cfg.GuestProbe.VMInclude, shells into the guest
// over VMBus once (guestprobe.Sample — a single Invoke-Command session
// gathering both facts, not two; PowerShell Direct sessions are the
// expensive part at fleet scale, not what's queried inside one) and
// exports vm.guest.filesystem.used_percent per fixed volume plus
// vm.guest.memory.used_percent. Reuses the disk map's VM enumeration
// (internal/hyperv.BuildDiskMap) rather than a separate Get-VM call. One
// VM's probe failing (e.g. missing guest Integration Services) does not
// block the others — logged and skipped, same fallback philosophy as
// buildDiskMap/sampleAndExport above.
func sampleGuestProbe(ctx context.Context, cfg *config.HostCompanionConfig, state *diskMapState, fsGauge, memGauge metric.Float64Gauge) {
	m := state.get()
	if m == nil {
		log.Printf("guest probe sample skipped: disk map not yet built")
		return
	}

	cred, err := creds.NewReader().Read(cfg.GuestProbe.CredentialName)
	if err != nil {
		log.Printf("guest probe: reading credential %q: %v", cfg.GuestProbe.CredentialName, err)
		return
	}

	for vmID, entry := range m.ByID {
		if !matchesInclude(entry.VMName, cfg.GuestProbe.VMInclude) {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, cfg.GuestProbe.SampleTimeout)
		sample, err := guestprobe.Sample(probeCtx, vmID, cred)
		cancel()
		if err != nil {
			log.Printf("guest probe: vm=%s: %v", entry.VMName, err)
			continue
		}
		for _, s := range sample.Filesystem {
			pct, ok := s.UsedPercent()
			if !ok {
				continue
			}
			fsGauge.Record(ctx, pct,
				metric.WithAttributes(
					attribute.String("vm.name", entry.VMName),
					attribute.String("drive_letter", s.DriveLetter),
				))
		}
		if pct, ok := sample.Memory.UsedPercent(); ok {
			memGauge.Record(ctx, pct, metric.WithAttributes(attribute.String("vm.name", entry.VMName)))
		}
	}
}

// matchesInclude reports whether name matches at least one filepath.Match
// glob pattern in include (e.g. "WebServer*"). A malformed pattern is
// logged and skipped rather than aborting the whole match, since a config
// typo shouldn't silently disable every VM's probe.
func matchesInclude(name string, include []string) bool {
	for _, pattern := range include {
		ok, err := filepath.Match(pattern, name)
		if err != nil {
			log.Printf("guest probe: invalid vm_include pattern %q: %v", pattern, err)
			continue
		}
		if ok {
			return true
		}
	}
	return false
}
