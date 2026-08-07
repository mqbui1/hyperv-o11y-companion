# hyperv-o11y-companion

Field-delivered Hyper-V monitoring solution for **Splunk Observability
Cloud**, which has no native Hyper-V integration. Consolidates two things
that used to live in separate repos/scripts into one place:

1. **Two Windows Services** (`cmd/`) — a consolidated replacement for the
   customer's four independently-scheduled PowerShell script pairs
   (`collect-scvmm-metrics.ps1` / `run-collect-scvmm-metrics.ps1`,
   `enrich-vm-guest-os.ps1` / `run-enrich-vm-guest-os.ps1`,
   `build-hyperv-vm-disk-map.ps1` / `collect-hyperv-vm-disk.ps1`), plus the
   the Tier 1.5 PowerShell Direct guest probe (implemented, disabled by
   default pending go/no-go — see below). Zero Windows Task
   Scheduler entries.
2. **OTel Collector configs + Terraform dashboards/detectors** (`otel-collector/`,
   `terraform/`) — the host-side (Tier 1) collection pipeline and the
   dashboards/detectors provisioned from it. Formerly the standalone
   `hyperv-o11y-accelerator` repo; merged here so the whole solution
   (code + config + IaC + docs) lives in one place.

See `docs/architecture.md` for the full five-tier model and
`docs/known-gaps-remediation.md` for how each of the 10 real customer-POC
gaps is addressed (8 solved, 2 open — see "Known gaps" below).

**Important customer constraint:** this customer has explicitly ruled out
deploying any collector inside guest VMs, opt-in or otherwise. Tier 2
(`otel-collector/guest-vm-config.yaml`) is kept in this repo for reference
only (e.g. future engagements without this constraint) and is **not** part
of the proposed solution here. Gaps #2 (static-memory VM memory pressure)
and #4 (guest filesystem used %) are therefore tracked as **open**, pending
a go/no-go decision on Tier 1.5 (PowerShell Direct guest probe —
implemented in `internal/guestprobe`, wired into `host-companion`, but
shipped with `guest_probe.enabled: false`; see
`docs/phase3-guest-probe-plan.md`), which requires nothing to be deployed
inside the guest.

## Repo layout

```
cmd/                             Windows Service binaries
  scvmm-poller/                  Tier 0 — gaps #1, #8
  host-companion/                Tier 1 companion — gap #3, #5
internal/                        Shared Go packages (config, creds, hyperv,
                                  diskattr, guestos, metadata, metricsexport,
                                  scvmm, winsvc)
installer/                       WiX v4 MSI installer (both services)
otel-collector/
  hypervisor-host-config.yaml    Tier 1 — deploy on every Hyper-V host
                                  (gaps #6, #7, #9, #10)
  guest-vm-config.yaml           Tier 2 — RULED OUT for this customer,
                                  reference only, do not deploy
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
  known-gaps-remediation.md      All 10 customer-POC gaps, mapped to fixes
  limitations.md                 What this solution cannot do, and why
  deployment-guide.md            Delivery, install, configure, test/verify
  parity-testing-and-cutover.md  Shadow -> diff -> cutover plan (old scripts
                                  -> new services)
  phase3-guest-probe-plan.md     Tier 1.5 (implemented, gap #4; disabled by
                                  default pending go/no-go)
  nested-hyperv-azure-test-plan.md  Real-hardware validation plan (Azure)
```

## Known gaps (10 from a real customer POC)

| # | Gap | Status |
|---|---|---|
| 1 | No power-state/availability monitoring | Solved — `hyperv-scvmm-poller` |
| 2 | Static-memory VMs invisible to memory pressure | **Open** — Tier 1.5 doesn't cover this metric yet (see `docs/phase3-guest-probe-plan.md`) |
| 3 | ~20% of VHD instances unattributed | Solved (accepted residual) — `hyperv-host-companion` |
| 4 | No guest filesystem used % visible | **Open** — Tier 1.5 implemented, disabled pending go/no-go |
| 5 | Disk latency unit unconfirmed | Solved — empirically verified, ×1000 scale correction |
| 6 | Malformed `vm.name` from Perfmon `#N` suffixing | Solved — `otel-collector/hypervisor-host-config.yaml` |
| 7 | ~20% of VMs missing network series | Solved — wrong counter name fixed |
| 8 | `guest_os` accuracy issues | Solved — `hyperv-scvmm-poller` |
| 9 | VMMS load from failed live migrations | Solved — event-to-metric `count` connector |
| 10 | poc -> production cutover | Solved by design — environment-agnostic filters |

See `docs/known-gaps-remediation.md` for the full per-gap writeup.

## Services (`cmd/`)

| Binary | Runs where | Replaces | Status |
|---|---|---|---|
| `scvmm-poller` | Central SCVMM console box (`CULSPLUNKO11Y01`) | `collect-scvmm-metrics.ps1` + `enrich-vm-guest-os.ps1` and their wrappers/scheduled tasks | **Phase 1 — scaffolded** |
| `host-companion` | Every Hyper-V host, alongside the Splunk OTel Collector | `build-hyperv-vm-disk-map.ps1` + `collect-hyperv-vm-disk.ps1` (Phase 2, scaffolded here), and the PowerShell Direct guest probe (Phase 3, implemented in `internal/guestprobe`, disabled by default — see `docs/phase3-guest-probe-plan.md`) | **Phase 2 — scaffolded; Phase 3 — implemented, disabled pending go/no-go** |

Both binaries are installed as native Windows Services (`golang.org/x/sys/windows/svc`)
via an MSI (see `installer/`, WiX v4 source with a two-feature scaffold — one
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
