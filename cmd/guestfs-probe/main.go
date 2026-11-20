// guestfs-probe is Tier 1.6: a read-only, host-side probe of a guest's
// filesystem usage, parsed directly out of its VHDX file (see
// internal/guestfs). Unlike Tier 1.5 (internal/guestprobe, PowerShell
// Direct), this works for both Windows and Linux guests and never runs
// anything inside the guest at all.
//
// Deliberately a separate binary/Windows Service from cmd/host-companion,
// not a third ticker added to it — a bug in this new VHDX/GPT/filesystem
// parsing code can crash or misbehave without taking down Tier 1/1.5,
// which keep running in host-companion's own process untouched.
package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/splunk/hyperv-o11y-companion/internal/config"
	"github.com/splunk/hyperv-o11y-companion/internal/guestfs"
	"github.com/splunk/hyperv-o11y-companion/internal/hyperv"
	"github.com/splunk/hyperv-o11y-companion/internal/metricsexport"
	"github.com/splunk/hyperv-o11y-companion/internal/winsvc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func main() {
	configPath := flag.String("config", `C:\Program Files\Splunk\HyperVO11yCompanion\config\guestfs-probe.yaml`, "path to guestfs-probe.yaml")
	flag.Parse()

	cfg, err := config.LoadGuestFSProbe(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	err = winsvc.Run("hyperv-guestfs-probe", func(ctx context.Context) error {
		return runService(ctx, cfg)
	})
	if err != nil {
		log.Fatalf("service exited with error: %v", err)
	}
}

// diskMapState guards the shared in-memory disk map between the builder
// and sampler — same pattern as cmd/host-companion/main.go's diskMapState,
// duplicated rather than shared since these are two independent services.
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

func runService(ctx context.Context, cfg *config.GuestFSProbeServiceConfig) error {
	exp, err := metricsexport.New(ctx, "hyperv-guestfs-probe", cfg.OTLP.Endpoint, cfg.OTLP.Insecure)
	if err != nil {
		return err
	}
	defer exp.Shutdown(ctx)

	// Same metric name Tier 1.5 uses for gap #4 — this is a second,
	// independent producer of it (root filesystem only, no
	// drive_letter attribute), not a competing/renamed metric.
	fsUsedGauge, err := exp.Meter.Float64Gauge("vm.guest.filesystem.used_percent",
		metric.WithDescription("Guest root filesystem used space (%), read from the VHDX on the host — Tier 1.6, gap #4 (Linux-capable, zero guest footprint)"),
		metric.WithUnit("%"))
	if err != nil {
		return err
	}

	// Coverage-roadmap telemetry: which v1-unsupported layout blocked a
	// probe, and how often. Drives which format (LVM? ext4/btrfs?) gets
	// prioritized for v2, from real fleet data instead of a guess — see
	// docs/architecture-gaps-overview.md.
	skipCounter, err := exp.Meter.Int64Counter("vm.guestfs.probe.skip",
		metric.WithDescription("Count of guestfs probe cycles skipped for a VM, tagged by skip_reason (differencing, lvm, mbr, encrypted, ext4_btrfs, no_linux_partition, panic, other)"))
	if err != nil {
		return err
	}

	// 2i: probe failure observability — RC asked for these before 2a/2b so
	// real skip_reason counts from the fleet size those items before code is
	// written. probeFailure counts each failed attempt; probeStale reports
	// how stale the last-known-good data point is.
	probeFailure, err := exp.Meter.Int64Counter("hyperv.guest_probe.failure",
		metric.WithDescription("Count of failed guest filesystem probe attempts, tagged by vm.name and reason"))
	if err != nil {
		return err
	}
	probeStale, err := exp.Meter.Float64Gauge("hyperv.guest_probe.stale_seconds",
		metric.WithDescription("Seconds since the last successful guest filesystem probe for this VM"),
		metric.WithUnit("s"))
	if err != nil {
		return err
	}

	// lastSuccess tracks the wall-clock time of the last successful probe per
	// VM name. Single-goroutine loop — no mutex needed.
	lastSuccess := make(map[string]time.Time)

	state := &diskMapState{}
	buildDiskMap(ctx, cfg, state)

	buildTicker := time.NewTicker(cfg.DiskMap.BuildInterval)
	defer buildTicker.Stop()

	var probeTicker *time.Ticker
	if cfg.GuestFS.Enabled && len(cfg.GuestFS.VMInclude) > 0 {
		probeTicker = time.NewTicker(cfg.GuestFS.SampleInterval)
		defer probeTicker.Stop()
	}
	probeC := func() <-chan time.Time {
		if probeTicker == nil {
			return nil // nil channel: never selected, cleanly disabling the tier
		}
		return probeTicker.C
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-buildTicker.C:
			buildDiskMap(ctx, cfg, state)
		case <-probeC:
			sampleGuestFS(ctx, cfg, state, fsUsedGauge, skipCounter, probeFailure, probeStale, lastSuccess)
		}
	}
}

func buildDiskMap(ctx context.Context, cfg *config.GuestFSProbeServiceConfig, state *diskMapState) {
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

// sampleGuestFS closes gap #4 for Linux (and Windows) guests with zero
// in-guest footprint: for each VM in the disk map matching
// cfg.GuestFS.VMInclude, opens its VHDX read-only on the host
// (internal/guestfs.FilesystemUsedPercent) and exports
// vm.guest.filesystem.used_percent. A VM with an unsupported layout
// (differencing disk, LVM, non-XFS filesystem) or any other read error is
// logged, counted (skipCounter, tagged by skip_reason), and skipped —
// never fatal to the other VMs' probe cycle, same fallback philosophy as
// host-companion's sampleGuestProbe.
//
// v1 limitation: hyperv.DiskMap.ByID keeps only the last VMDiskEntry seen
// per VM (it was originally just a .vmgs-instance fallback, see
// internal/hyperv/types.go), so a VM with more than one attached VHDX is
// not guaranteed to resolve to its boot/root disk here. Fine for
// single-disk VMs (the common case); multi-disk VMs are a known follow-up,
// not a v1 requirement.
func sampleGuestFS(ctx context.Context, cfg *config.GuestFSProbeServiceConfig, state *diskMapState, fsGauge metric.Float64Gauge, skipCounter metric.Int64Counter, probeFailure metric.Int64Counter, probeStale metric.Float64Gauge, lastSuccess map[string]time.Time) {
	m := state.get()
	if m == nil {
		log.Printf("guestfs probe sample skipped: disk map not yet built")
		return
	}

	for _, entry := range m.ByID {
		if !matchesInclude(entry.VMName, cfg.GuestFS.VMInclude) {
			continue
		}
		if entry.Path == "" {
			continue
		}
		probeOneVM(ctx, entry, fsGauge, skipCounter, probeFailure, probeStale, lastSuccess)
	}
}

// probeOneVM probes a single VM's VHDX and recovers from any panic while
// doing so. A malformed/corrupt VHDX is a certainty at fleet scale, not
// an edge case — one VM's bad disk image must not crash the probe cycle
// (and take every other VM's probe down with it) or crash-loop the whole
// service. A recovered panic is counted under skip_reason="panic", same
// as any other classified skip, so it shows up in the same
// coverage-roadmap telemetry rather than silently vanishing into a log
// line.
//
// 2i: on success, records the success time in lastSuccess and emits
// probeStale=0. On any failure (including panic), increments probeFailure
// and emits probeStale = seconds since last success (24h sentinel if this
// VM has never succeeded).
func probeOneVM(ctx context.Context, entry hyperv.VMDiskEntry, fsGauge metric.Float64Gauge, skipCounter metric.Int64Counter, probeFailure metric.Int64Counter, probeStale metric.Float64Gauge, lastSuccess map[string]time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("guestfs probe: vm=%s: recovered from panic: %v", entry.VMName, r)
			skipCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("skip_reason", "panic")))
			probeFailure.Add(ctx, 1, metric.WithAttributes(
				attribute.String("vm.name", entry.VMName),
				attribute.String("reason", "other"),
			))
			recordProbeStale(ctx, entry.VMName, lastSuccess, probeStale)
		}
	}()

	pct, err := guestfs.FilesystemUsedPercent(entry.Path)
	if err != nil {
		reason := guestfs.ClassifySkipReason(err)
		log.Printf("guestfs probe: vm=%s: %v", entry.VMName, err)
		skipCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("skip_reason", string(reason))))
		probeFailure.Add(ctx, 1, metric.WithAttributes(
			attribute.String("vm.name", entry.VMName),
			attribute.String("reason", "other"),
		))
		recordProbeStale(ctx, entry.VMName, lastSuccess, probeStale)
		return
	}
	lastSuccess[entry.VMName] = time.Now()
	fsGauge.Record(ctx, pct, metric.WithAttributes(attribute.String("vm.name", entry.VMName)))
	probeStale.Record(ctx, 0, metric.WithAttributes(attribute.String("vm.name", entry.VMName)))
}

// recordProbeStale emits hyperv.guest_probe.stale_seconds for vmName. If the
// VM has never had a successful probe, emits 86400 (24h) as a sentinel so
// the "> 15 minutes" detector fires immediately on first rollout.
func recordProbeStale(ctx context.Context, vmName string, lastSuccess map[string]time.Time, gauge metric.Float64Gauge) {
	var stale float64
	if t, ok := lastSuccess[vmName]; ok {
		stale = time.Since(t).Seconds()
	} else {
		stale = 86400 // never succeeded — fire the detector immediately
	}
	gauge.Record(ctx, stale, metric.WithAttributes(attribute.String("vm.name", vmName)))
}

// matchesInclude reports whether name matches at least one filepath.Match
// glob pattern in include — same helper as
// cmd/host-companion/main.go:matchesInclude, duplicated (not imported)
// since these are two independent main packages.
func matchesInclude(name string, include []string) bool {
	for _, pattern := range include {
		ok, err := filepath.Match(pattern, name)
		if err != nil {
			log.Printf("guestfs probe: invalid vm_include pattern %q: %v", pattern, err)
			continue
		}
		if ok {
			return true
		}
	}
	return false
}
