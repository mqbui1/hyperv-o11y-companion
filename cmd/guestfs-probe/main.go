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
	"sort"
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
// in-guest footprint. For each VM in the disk map matching VMInclude, it
// tries every attached VHDX in controller order (lowest ControllerNumber,
// ControllerLocation first) and reports vm.guest.filesystem.used_percent
// for the first disk that yields a readable filesystem. Skip counts are
// recorded per disk attempt; 2i failure/stale metrics are only emitted at
// the VM level after all of that VM's disks have been exhausted.
func sampleGuestFS(ctx context.Context, cfg *config.GuestFSProbeServiceConfig, state *diskMapState, fsGauge metric.Float64Gauge, skipCounter metric.Int64Counter, probeFailure metric.Int64Counter, probeStale metric.Float64Gauge, lastSuccess map[string]time.Time) {
	m := state.get()
	if m == nil {
		log.Printf("guestfs probe sample skipped: disk map not yet built")
		return
	}

	for vmName, disks := range groupByVM(m) {
		if !matchesInclude(vmName, cfg.GuestFS.VMInclude) {
			continue
		}
		probeVMDisks(ctx, vmName, disks, fsGauge, skipCounter, probeFailure, probeStale, lastSuccess)
	}
}

// groupByVM groups all entries in diskMap.ByPath by VMName, returning a
// map of VMName -> disks sorted by (ControllerNumber, ControllerLocation).
// Entries with an empty VMName or Path are excluded.
func groupByVM(m *hyperv.DiskMap) map[string][]hyperv.VMDiskEntry {
	grouped := map[string][]hyperv.VMDiskEntry{}
	for _, e := range m.ByPath {
		if e.VMName == "" || e.Path == "" {
			continue
		}
		grouped[e.VMName] = append(grouped[e.VMName], e)
	}
	for name := range grouped {
		sort.Slice(grouped[name], func(i, j int) bool {
			a, b := grouped[name][i], grouped[name][j]
			if a.ControllerNumber != b.ControllerNumber {
				return a.ControllerNumber < b.ControllerNumber
			}
			return a.ControllerLocation < b.ControllerLocation
		})
	}
	return grouped
}

// probeVMDisks tries each disk in controller order. It records a
// skip_reason counter for each disk that fails, then stops at the first
// disk that returns a readable filesystem percent. If all disks fail,
// it records the 2i failure counter and stale gauge for the VM.
func probeVMDisks(ctx context.Context, vmName string, disks []hyperv.VMDiskEntry, fsGauge metric.Float64Gauge, skipCounter metric.Int64Counter, probeFailure metric.Int64Counter, probeStale metric.Float64Gauge, lastSuccess map[string]time.Time) {
	for _, disk := range disks {
		pct, skipReason, panicked := tryOneDisk(disk.Path)
		if panicked {
			log.Printf("guestfs probe: vm=%s disk=%s: recovered from panic", vmName, disk.Path)
			skipCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("skip_reason", "panic")))
			continue
		}
		if skipReason != "" {
			log.Printf("guestfs probe: vm=%s disk=%s: %s", vmName, disk.Path, skipReason)
			skipCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("skip_reason", skipReason)))
			continue
		}
		// First disk that succeeds — record and return.
		lastSuccess[vmName] = time.Now()
		fsGauge.Record(ctx, pct, metric.WithAttributes(attribute.String("vm.name", vmName)))
		probeStale.Record(ctx, 0, metric.WithAttributes(attribute.String("vm.name", vmName)))
		return
	}
	// All disks failed — emit 2i failure + stale for this VM.
	probeFailure.Add(ctx, 1, metric.WithAttributes(
		attribute.String("vm.name", vmName),
		attribute.String("reason", "other"),
	))
	recordProbeStale(ctx, vmName, lastSuccess, probeStale)
}

// tryOneDisk probes a single VHDX path with panic recovery.
// Returns (pct, skipReason, panicked). On success skipReason is "".
func tryOneDisk(path string) (pct float64, skipReason string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	p, err := guestfs.FilesystemUsedPercent(path)
	if err != nil {
		return 0, string(guestfs.ClassifySkipReason(err)), false
	}
	return p, "", false
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
