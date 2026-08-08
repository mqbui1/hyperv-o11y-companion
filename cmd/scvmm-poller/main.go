// scvmm-poller is the Phase-1 consolidated replacement for
// collect-scvmm-metrics.ps1 + enrich-vm-guest-os.ps1 (and their wrapper
// scripts / scheduled tasks). Runs as a Windows Service on the central SCVMM
// console box, with two independent tickers:
//
//   - metrics: polls Get-SCVMHost/Get-SCVirtualMachine, exports
//     hyperv_host_up/hyperv_vm_up + host capacity metrics via OTLP.
//   - guest_os: polls the same VM list's OperatingSystem field and writes
//     the guest_os dimension property via the SignalFx metadata API.
//
// See README.md for the architecture rationale and config/scvmm-poller.yaml
// for the config schema.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/splunk/hyperv-o11y-companion/internal/config"
	"github.com/splunk/hyperv-o11y-companion/internal/creds"
	"github.com/splunk/hyperv-o11y-companion/internal/guestos"
	"github.com/splunk/hyperv-o11y-companion/internal/metadata"
	"github.com/splunk/hyperv-o11y-companion/internal/metricsexport"
	"github.com/splunk/hyperv-o11y-companion/internal/scvmm"
	"github.com/splunk/hyperv-o11y-companion/internal/winsvc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func main() {
	configPath := flag.String("config", `C:\Program Files\Splunk\HyperVO11yCompanion\config\scvmm-poller.yaml`, "path to scvmm-poller.yaml")
	flag.Parse()

	cfg, err := config.LoadPoller(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	err = winsvc.Run("hyperv-scvmm-poller", func(ctx context.Context) error {
		return runService(ctx, cfg)
	})
	if err != nil {
		log.Fatalf("service exited with error: %v", err)
	}
}

func runService(ctx context.Context, cfg *config.PollerConfig) error {
	credReader := creds.NewReader()

	scvmmCred, err := credReader.Read(cfg.Scvmm.CredentialName)
	if err != nil {
		return err
	}
	splunkCred, err := credReader.Read(cfg.Splunk.CredentialName)
	if err != nil {
		return err
	}

	client := scvmm.New(cfg.Scvmm.Server, scvmmCred.Username, scvmmCred.Password, cfg.Scvmm.Cluster)
	metaClient := metadata.New(cfg.Splunk.Realm, splunkCred.Password) // access token stored as the credential's "password"

	exp, err := metricsexport.New(ctx, "hyperv-scvmm-poller", cfg.OTLP.Endpoint, cfg.OTLP.Insecure)
	if err != nil {
		return err
	}
	defer exp.Shutdown(ctx)

	hostUp, err := exp.Meter.Float64Gauge("hyperv_host_up")
	if err != nil {
		return err
	}
	vmUp, err := exp.Meter.Float64Gauge("hyperv_vm_up")
	if err != nil {
		return err
	}
	hostMemUsed, err := exp.Meter.Float64Gauge("hyperv_host_memory_used_mb")
	if err != nil {
		return err
	}

	metricsTicker := time.NewTicker(cfg.Metrics.Interval)
	defer metricsTicker.Stop()
	guestOSTicker := time.NewTicker(cfg.GuestOS.Interval)
	defer guestOSTicker.Stop()

	// Run once immediately on startup, then on each ticker interval.
	pollMetrics(ctx, client, hostUp, vmUp, hostMemUsed)
	pollGuestOS(ctx, client, metaClient, cfg)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-metricsTicker.C:
			pollMetrics(ctx, client, hostUp, vmUp, hostMemUsed)
		case <-guestOSTicker.C:
			pollGuestOS(ctx, client, metaClient, cfg)
		}
	}
}

// pollMetrics closes gap #1 (power state / availability): hyperv_host_up /
// hyperv_vm_up are 0/1 gauges sourced from SCVMM's OverallState/Status,
// something the host-side windowsperfcounters receiver structurally cannot
// see (a powered-off VM just stops emitting Perfmon data, indistinguishable
// from a missed scrape). See docs/known-gaps-remediation.md gap #1.
func pollMetrics(ctx context.Context, client *scvmm.Client, hostUp, vmUp, hostMemUsed metric.Float64Gauge) {
	hosts, err := client.Hosts(ctx)
	if err != nil {
		log.Printf("scvmm hosts poll failed: %v", err)
	} else {
		for _, h := range hosts {
			up := 0.0
			if h.Overall == "Responding" || h.Overall == "OK" {
				up = 1.0
			}
			hostUp.Record(ctx, up, metric.WithAttributes(attribute.String("host.name", h.Name)))
			hostMemUsed.Record(ctx, h.TotalMemoryMB-h.AvailableMemoryMB, metric.WithAttributes(attribute.String("host.name", h.Name)))
		}
	}

	vms, err := client.VMs(ctx)
	if err != nil {
		log.Printf("scvmm vms poll failed: %v", err)
		return
	}
	for _, v := range vms {
		up := 0.0
		if v.Status == "Running" {
			up = 1.0
		}
		vmUp.Record(ctx, up, metric.WithAttributes(
			attribute.String("vm.name", v.Name),
			attribute.String("hypervisor.host.name", v.VMHostName),
		))
	}
}

// pollGuestOS closes gap #8 (guest_os accuracy): classify each VM's SCVMM
// OperatingSystem field and write the guest_os dimension property, in-process
// via internal/guestos + internal/metadata instead of shelling out to
// enrich-vm-guest-os.ps1. Duplicate VM names are skipped, not clobbered, same
// as the original script.
func pollGuestOS(ctx context.Context, client *scvmm.Client, metaClient *metadata.Client, cfg *config.PollerConfig) {
	vms, err := client.VMs(ctx)
	if err != nil {
		log.Printf("scvmm vms poll (guest_os) failed: %v", err)
		return
	}

	seen := map[string]int{}
	for _, v := range vms {
		seen[v.Name]++
	}

	set, unchanged, skipped, errs := 0, 0, 0, 0
	for _, v := range vms {
		if seen[v.Name] > 1 {
			skipped++
			continue
		}
		g := guestos.Classify(v.OperatingSystem)
		source := "scvmm"
		if g == "unknown" && cfg.GuestOS.NameHeuristic {
			if nh := guestos.NameHeuristic(v.Name, cfg.GuestOS.LinuxNameMarkers); nh != "" {
				g, source = nh, "heuristic"
			}
		}
		changed, err := metaClient.SetGuestOS(ctx, v.Name, g, v.OperatingSystem, source)
		if err != nil {
			log.Printf("guest_os update failed for %s: %v", v.Name, err)
			errs++
			continue
		}
		if changed {
			set++
		} else {
			unchanged++
		}
	}
	log.Printf("guest_os: set=%d unchanged=%d dup-skipped=%d errors=%d", set, unchanged, skipped, errs)
}
