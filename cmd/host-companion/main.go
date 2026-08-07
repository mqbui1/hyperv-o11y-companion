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
	"strings"
	"sync"
	"time"

	"github.com/rccl/hyperv-o11y-companion/internal/config"
	"github.com/rccl/hyperv-o11y-companion/internal/diskattr"
	"github.com/rccl/hyperv-o11y-companion/internal/hyperv"
	"github.com/rccl/hyperv-o11y-companion/internal/metricsexport"
	"github.com/rccl/hyperv-o11y-companion/internal/winsvc"
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

	state := &diskMapState{}
	buildDiskMap(ctx, cfg, state) // populate once at startup before the first sample cycle

	buildTicker := time.NewTicker(cfg.DiskMap.BuildInterval)
	defer buildTicker.Stop()
	sampleTicker := time.NewTicker(cfg.DiskMetrics.SampleInterval)
	defer sampleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-buildTicker.C:
			buildDiskMap(ctx, cfg, state)
		case <-sampleTicker.C:
			sampleAndExport(ctx, cfg, state, latencyGauge, readGauge, writeGauge)
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
