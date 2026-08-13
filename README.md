# hyperv-o11y-companion

Field-delivered Hyper-V monitoring solution for **Splunk Observability
Cloud**, which has no native Hyper-V integration. Consolidates two things
that used to live in separate repos/scripts into one place:

1. **Three Windows Services** (`cmd/`) — a consolidated replacement for the
   customer's four independently-scheduled PowerShell script pairs
   (`collect-scvmm-metrics.ps1` / `run-collect-scvmm-metrics.ps1`,
   `enrich-vm-guest-os.ps1` / `run-enrich-vm-guest-os.ps1`,
   `build-hyperv-vm-disk-map.ps1` / `collect-hyperv-vm-disk.ps1`), plus the
   Tier 1.5 PowerShell Direct guest probe (Windows guests, implemented,
   disabled by default pending go/no-go — see below) and Tier 1.6
   (`cmd/guestfs-probe`, Windows AND Linux guests, VHDX host-side read,
   zero guest interaction — see below). Zero Windows Task Scheduler
   entries.
2. **OTel Collector configs + Terraform dashboards/detectors** (`otel-collector/`,
   `terraform/`) — the host-side (Tier 1) collection pipeline and the
   dashboards/detectors provisioned from it. Formerly the standalone
   `hyperv-o11y-accelerator` repo; merged here so the whole solution
   (code + config + IaC + docs) lives in one place.

See `docs/architecture.md` for the full five-tier model and
`docs/known-gaps-remediation.md` for how each of the 10 real customer-POC
gaps is addressed (10 solved — 2 of those pending fleet-wide validation, 2
implemented but not yet live-tested end-to-end, 1 unconfirmed against the
original POC's own cluster — see "Known gaps" below).

**Important customer constraint:** this customer has explicitly ruled out
deploying any collector inside guest VMs, opt-in or otherwise — confirmed to
apply to every guest OS, including their mix of RHEL 7/8/9 Linux guests,
not just Windows. Gaps #2 (static-memory VM memory pressure) and #4 (guest
filesystem used %) are solved via Tier 1.5 (PowerShell Direct guest
probe — implemented in `internal/guestprobe`, wired into `host-companion`,
mechanism-validated on a real nested-Hyper-V test, **Windows guests only**),
which requires nothing to be deployed inside the guest. It ships with
`guest_probe.enabled: false` and stays that way pending fleet-wide go/no-go
validation (session load at scale, real-fleet Integration Services
coverage, shared-credential security review — see
`docs/phase3-guest-probe-plan.md`).

Gap #4 also has a **Linux-capable** solution: Tier 1.6 (`cmd/guestfs-probe`,
`internal/guestfs`) reads the guest's filesystem usage directly out of its
VHDX file on the host — GPT partition table + filesystem superblock parsing,
zero guest interaction of any kind (not even a one-off command). A fully
independent Windows Service from `host-companion`, so it can't affect
Tier 1/1.5. v1 supports GPT-partitioned VHDX disks with an XFS root
filesystem (RHEL 7/8/9's default) only; **validated end-to-end against a
real, running Rocky Linux 9 guest** on nested-Hyper-V test infrastructure —
this real-hardware pass found and fixed a genuine defect (root-partition
misidentification on a standard non-LVM layout; see
`docs/known-gaps-remediation.md` gap #4). Still ships with
`guest_fs_probe.enabled: false` by default pending a fleet pilot. Gap #2
(guest memory) has no
guestfs-probe equivalent, but has its own zero-code fix for any guest OS:
enabling Hyper-V Dynamic Memory with `Min = Max = Startup` activates the
existing Tier 1 `vm.memory.current_pressure` counter — see
`docs/known-gaps-remediation.md` gap #2.

See `docs/capabilities-and-metrics.md` for the full generic (customer-agnostic)
metrics catalog and what's already visualized in Splunk Observability Cloud
out of the box, organized by tier.

## Repo layout

```
cmd/                             Windows Service binaries
  scvmm-poller/                  Tier 0 — gaps #1, #8
  host-companion/                Tier 1 companion — gap #3, #5
  guestfs-probe/                 Tier 1.6 — gap #4, Linux-capable, zero guest footprint
internal/                        Shared Go packages (config, creds, hyperv,
                                  diskattr, guestos, guestfs, guestprobe,
                                  metadata, metricsexport, scvmm, winsvc)
installer/                       WiX v4 MSI installer (all three services)
otel-collector/
  hypervisor-host-config.yaml    Tier 1 — deploy on every Hyper-V host
                                  (gaps #6, #7, #9, #10)
  test/                          Local Docker test harness (no Windows host
                                  or Splunk account needed)
terraform/
  main.tf                        signalfx provider + dashboard group
  dashboards.tf                  Hypervisor Overview + VM Detail dashboards
  detectors.tf                   VM health, CPU, memory, storage-latency,
                                  VMMS migration-failure detectors
docs/
  architecture.md                Five-tier model, resource-attribute strategy
  architecture-gaps-overview.md  Combined architecture + gap-mapping summary
  capabilities-and-metrics.md    Generic metrics catalog + what's visualized
                                  in Splunk Observability Cloud, by tier
  known-gaps-remediation.md      All 10 customer-POC gaps, mapped to fixes
  limitations.md                 What this solution cannot do, and why
  deployment-guide.md            Delivery, install, configure, test/verify
  parity-testing-and-cutover.md  Shadow -> diff -> cutover plan (old scripts
                                  -> new services)
  phase3-guest-probe-plan.md     Tier 1.5 (implemented, gap #4, Windows
                                  guests; disabled by default pending go/no-go)
  nested-hyperv-azure-test-plan.md  Real-hardware validation plan (Azure)
```

## Tier 1 — host-side OTel Collector

The foundation tier, and where this solution started (as the standalone
`hyperv-o11y-accelerator` repo) before the five-tier model grew around it —
see `docs/architecture.md`. A generic OpenTelemetry
`windowsperfcounters`/`windowseventlogreceiver` pipeline
(`otel-collector/hypervisor-host-config.yaml`), deployed identically on
every Hyper-V host via the Splunk Distribution of the OpenTelemetry
Collector for Windows — no custom binary, no credentials beyond the ingest
token (`SPLUNK_ACCESS_TOKEN`/`SPLUNK_REALM`). Covers hypervisor-visible
resource-allocation metrics (vCPU %, assigned memory, virtual disk file
I/O, vNIC throughput) plus the malformed-`vm.name` fix (gap #6), the
disk-latency scale correction (gap #5), the corrected network counter
names (gap #7), and the VMMS migration-failure event-to-metric conversion
(gap #9). Cheap — no extra billable hosts — but structurally cannot see
anything happening inside a guest's own OS; that's what Tier 1
companion/1.5 are for. This is what fills the "Hyper-V: Hypervisor
Overview" dashboard (`terraform/dashboards.tf`). See
`docs/capabilities-and-metrics.md` for the full metrics catalog and
`docs/deployment-guide.md` for the install steps (Splunk OTel Collector for
Windows MSI, config swap, env vars, service restart).

## Quick start

1. **Provision dashboards + detectors** (one-time, org-wide, needs an
   admin-scoped org token — not the ingest token used below):
   ```
   cd terraform
   cp terraform.tfvars.example terraform.tfvars   # fill in splunk_access_token + splunk_realm
   terraform init && terraform apply
   ```
2. **Install the Splunk OTel Collector for Windows on every Hyper-V host**
   (Tier 1) — install the MSI, replace its config with
   `otel-collector/hypervisor-host-config.yaml`, set
   `SPLUNK_ACCESS_TOKEN`/`SPLUNK_REALM`, restart the service.
3. **Install the Windows Services** (`scvmm-poller` on the SCVMM
   console box, `host-companion` on every Hyper-V host, and optionally
   `guestfs-probe` alongside `host-companion` for Linux guest filesystem
   visibility) via the MSI in `installer/` — see "Services" below.

Full step-by-step instructions, credential setup, and a test/verify
checklist: `docs/deployment-guide.md`.

## Known gaps (10 from a real customer POC)

| # | Gap | Status |
|---|---|---|
| 1 | No power-state/availability monitoring | Solved (implemented, not yet live-tested end-to-end) — `hyperv-scvmm-poller` |
| 2 | Static-memory VMs invisible to memory pressure | Solved (pending fleet-wide validation, Windows guests) — Tier 1.5, mechanism-validated on a real nested-Hyper-V test; ships `disabled` until fleet go/no-go. Zero-code alternative for any guest OS: Dynamic Memory `Min=Max=Startup` |
| 3 | ~20% of VHD instances unattributed | Solved (accepted residual) — `hyperv-host-companion` |
| 4 | No guest filesystem used % visible | Solved — Tier 1.5 (Windows guests, pending fleet-wide validation) or Tier 1.6 (Windows/Linux, VHDX host-side read, validated end-to-end against a real Linux guest); both ship `disabled` pending fleet pilot |
| 5 | Disk latency unit unconfirmed | Solved — empirically verified, ×1000 scale correction |
| 6 | Malformed `vm.name` from Perfmon `#N` suffixing | Solved — `otel-collector/hypervisor-host-config.yaml` |
| 7 | ~20% of VMs missing network series | Solved for a from-scratch deployment — wrong counter name fixed; unconfirmed whether this fully resolves the original POC's own cluster (see gap #7's "Still open" note) |
| 8 | `guest_os` accuracy issues | Solved (implemented, not yet live-tested end-to-end) — `hyperv-scvmm-poller` |
| 9 | VMMS load from failed live migrations | Solved — event-to-metric `count` connector |
| 10 | poc -> production cutover | Solved by design — environment-agnostic filters |

See `docs/known-gaps-remediation.md` for the full per-gap writeup.

## Services (`cmd/`)

| Binary | Runs where | Replaces | Status |
|---|---|---|---|
| `scvmm-poller` | Central SCVMM console box (e.g. `SCVMM-CONSOLE-01`) | `collect-scvmm-metrics.ps1` + `enrich-vm-guest-os.ps1` and their wrappers/scheduled tasks | **Phase 1 — scaffolded** |
| `host-companion` | Every Hyper-V host, alongside the Splunk OTel Collector | `build-hyperv-vm-disk-map.ps1` + `collect-hyperv-vm-disk.ps1` (Phase 2, scaffolded here), and the PowerShell Direct guest probe (Phase 3, implemented in `internal/guestprobe`, disabled by default — see `docs/phase3-guest-probe-plan.md`) | **Phase 2 — scaffolded; Phase 3 — implemented, disabled pending go/no-go** |
| `guestfs-probe` | Every Hyper-V host with Linux (or Windows) guests needing filesystem usage, alongside the Splunk OTel Collector | Nothing pre-existing — new Tier 1.6 mechanism (`internal/guestfs`), a fully independent service from `host-companion` | **Implemented and validated end-to-end against a real Linux guest; disabled by default pending fleet pilot** |

All three binaries are installed as native Windows Services (`golang.org/x/sys/windows/svc`)
via an MSI (see `installer/`, WiX v4 source with a three-feature scaffold — one
feature per service, so the same MSI installs the right thing on the console
box vs. every Hyper-V host), so they start on boot, restart on failure, and
show up in `services.msc` — no Task Scheduler, no DPAPI secret files on disk.
Also see `docs/parity-testing-and-cutover.md` for the shadow → diff → cutover
plan for replacing the existing scripts/scheduled tasks with these services.

## Why Go still shells out to PowerShell for SCVMM

SCVMM has no REST API on-prem (only Azure Arc-enabled SCVMM would expose one),
and there is no Go SDK for the `VirtualMachineManager` PowerShell module. So
`internal/scvmm` invokes `powershell.exe -NoProfile -Command "... | ConvertTo-Json"`
as a narrow, single-purpose adapter and parses the JSON result — the same
underlying SCVMM cmdlets the existing scripts use
(`Get-SCVMHost`, `Get-SCVirtualMachine`), just called once per poll cycle from
a long-running process instead of a per-run script invocation. Everything
else (scheduling, retries, batching, OTLP export, credential handling,
dimension-API writes) is native Go — no other PowerShell dependency.

## Credentials

`scvmm-poller`'s SCVMM read-only account and Splunk access token are read
from Windows Credential Manager (`internal/creds`, generic credential type)
at service startup, not from DPAPI-encrypted files in `C:\ProgramData`. One
credential per secret, set once via `cmdkey`/Credential Manager UI, readable
only by the service account.

`host-companion` needs no credentials at all — it queries the local Hyper-V
host directly (`Get-VM`/`Get-VMHardDiskDrive`/`Get-Counter`, no remoting) and
exports to the host-local Splunk OTel Collector over plain OTLP, which
already holds the upstream Splunk credential via its own `signalfx`
exporter config.

`guestfs-probe` also needs no credentials at all, ever — it never talks to
the guest, only reads the VHDX file directly on the host.

## Phase 2 — `host-companion` disk metrics (gap #3)

Two tickers sharing one in-memory disk map (`internal/hyperv`,
`internal/diskattr`) instead of the JSON cache file the original two-script
pair (`build-hyperv-vm-disk-map.ps1` writes, `collect-hyperv-vm-disk.ps1`
reads) used to hand data between separate processes:

- **Disk map builder** (default hourly, `disk_map.build_interval`): shells
  out locally to `Get-VM | Get-VMHardDiskDrive`, same query as
  `build-hyperv-vm-disk-map.ps1`. On timeout/error, keeps the previous map
  rather than blocking or exporting unattributed metrics.
- **Disk metrics sampler** (default 60s, `disk_metrics.sample_interval`):
  samples the `Hyper-V Virtual Storage Device` counters directly via
  `Get-Counter`, resolves each instance to a VM via the disk map
  (`internal/diskattr.Resolve` — path-suffix match, `.vmgs` VM-Id fallback),
  and exports `vm.disk.{latency,read_bytes_sec,write_bytes_sec}`. Applies the
  empirically-confirmed seconds→ms scale correction to latency by default
  (`disk_metrics.latency_scale: 1000.0` — see gap #5). Unmatched instances
  (DVD/ISO/pass-through disks) are counted and logged, not guessed at —
  same accepted ~19–23% residual as today's scripts (77–81% match rate).

## guest_os delivery

Kept as a SignalFx metadata-API dimension property (`internal/metadata`),
matching the existing script's approach — it's zero-MTS and retroactively
applies to every metric already carrying that `vm` dimension value. Run on
its own (longer) interval, decoupled from the metric-push loop, so a
metadata-API hiccup never blocks metric ingestion.
