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
gaps is addressed (10 solved — 2 of those pending fleet-wide validation — see "Known gaps" below).

**Important customer constraint:** this customer has explicitly ruled out
deploying any collector inside guest VMs, opt-in or otherwise. Tier 2
(`otel-collector/guest-vm-config.yaml`) is kept in this repo for reference
only (e.g. future engagements without this constraint) and is **not** part
of the proposed solution here. Gaps #2 (static-memory VM memory pressure)
and #4 (guest filesystem used %) are solved via Tier 1.5 (PowerShell Direct
guest probe — implemented in `internal/guestprobe`, wired into
`host-companion`, mechanism-validated on a real nested-Hyper-V test), which
requires nothing to be deployed inside the guest. It ships with
`guest_probe.enabled: false` and stays that way pending fleet-wide go/no-go
validation (session load at scale, real-fleet Integration Services
coverage, shared-credential security review — see
`docs/phase3-guest-probe-plan.md`).

## Capabilities (generic — applies to any Hyper-V environment)

Everything below is independent of any single customer's constraints — it's
the full set of things this solution can do, organized by tier. Which tiers
you deploy depends on your own guest-VM policy (see "Choosing your tiers"
at the end of this section); nothing here requires a specific customer's
setup to be true.

| Capability | Tier | What you get | Requires |
|---|---|---|---|
| VM/host power-state (up/down) | 0 | `hyperv_vm_up`/`hyperv_host_up` gauges — the only way to detect a powered-off VM; Perfmon simply stops emitting, indistinguishable from a missed scrape | SCVMM management server, read-only SCVMM account |
| Guest OS classification | 0 | `guest_os` dimension property (SCVMM's OperatingSystem field → Secure Boot template fallback → optional naming heuristic), zero extra MTS cost, applies retroactively to every metric already carrying that VM | SCVMM |
| Host CPU/memory/disk/network | 1 | `host.*` metrics — physical host resource utilization | Splunk OTel Collector on every Hyper-V host |
| Hypervisor-level aggregate metrics | 1 | `hyperv.*` metrics (hypervisor CPU, VMMS health) | same as above |
| Per-VM CPU/memory/disk/network (host-visible) | 1 | `vm.*` metrics — vCPU %, assigned/pressure memory, virtual disk file I/O, vNIC throughput, all attributed per VM via Perfmon instance-string parsing | same as above |
| Per-VM disk latency/throughput attribution | 1 companion | `vm.disk.{latency,read_bytes_sec,write_bytes_sec}` — resolves live storage counters to VM names via an in-memory VHD-path map (`Get-VM \| Get-VMHardDiskDrive`), correcting for Perfmon's raw-path-only instance strings | `host-companion` Windows Service on every host |
| Failed live-migration alerting | 1 | `hyperv.vmms.migration_failures` — converts Event ID 21026 occurrences into an alertable metric (the general pattern for turning any event-log channel into a detector-eligible signal) | Splunk OTel Collector's `windowseventlogreceiver` + `count` connector |
| Guest filesystem used % | 1.5 (optional) | `vm.guest.filesystem.used_percent`, per fixed volume — **without deploying anything inside the guest** | Guest Integration Services running, a credential valid inside the guest, `host-companion` |
| Guest memory used % (incl. static-memory VMs) | 1.5 (optional) | `vm.guest.memory.used_percent` — guest's own OS-reported usage, so it works for static-memory VMs that Hyper-V's Dynamic-Memory-only "Current Pressure" counter can't see at all | same as above |
| Centralized event-log visibility | 1.6 (optional, config-only) | Guest/host event logs forwarded to one collector via native Windows Event Forwarding, no per-VM collector deployment | Windows Event Forwarding subscription setup |
| Full in-guest metrics (process/app-level, disk %, real memory, etc.) | 2 (optional) | Everything Tier 1.5 gives you, plus process/service state and application-level metrics — the most complete option | An OTel Collector installed inside every opted-in guest VM (each becomes a separately-billed host) |
| Dashboards | — | "Hypervisor Overview" (host-level, one row per host) and "VM Detail" (per-VM, including guest metrics if Tier 1.5/2 enabled) | `terraform/dashboards.tf` |
| Detectors | — | VM health critical, hypervisor CPU high, VM memory pressure high, VM storage latency high, guest filesystem/memory used high, VMMS migration failures | `terraform/detectors.tf` |
| Native Windows Service deployment | — | Both companion services start on boot, restart on failure, visible in `services.msc` — no Task Scheduler entries, no DPAPI secret files on disk | `installer/` (WiX v4 MSI) |
| Credential management | — | SCVMM/Splunk credentials read from Windows Credential Manager at service startup, one credential per secret, readable only by the service account | `internal/creds` |
| poc → production cutover | — | No config change required — `host.type` filters used throughout are environment-agnostic; cutover is just pointing the realm/access token at production | — |

### Choosing your tiers

- **Tiers 0, 1, and 1 companion** are the baseline for any Hyper-V
  environment — no guest-VM policy decision needed, deploy on the SCVMM
  console box and every physical host.
- **Tier 1.5** is the right choice if you want guest filesystem/memory
  visibility but don't want to deploy a collector inside guest VMs at all —
  it uses PowerShell Direct (`Invoke-Command -VMId`) over VMBus, so there's
  no guest network path, no guest firewall rule, and no agent process
  running inside the guest. Requires guest Integration Services and a
  valid in-guest credential.
- **Tier 2** is the right choice if guest-level visibility beyond
  filesystem/memory (process state, application metrics) is required and
  the extra per-VM billed-host cost is acceptable.
- **Tier 1.6** (Windows Event Forwarding) is additive to any of the above —
  it's a native Windows mechanism, not new code, for centralizing event
  logs without per-VM collector deployment.

See `docs/architecture.md` for the full tier diagram and resource-attribute
strategy, and `docs/deployment-guide.md` for install/configure steps
(including a pilot-enablement procedure for Tier 1.5).

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
| 2 | Static-memory VMs invisible to memory pressure | Solved (pending fleet-wide validation) — Tier 1.5, mechanism-validated on a real nested-Hyper-V test; ships `disabled` until fleet go/no-go |
| 3 | ~20% of VHD instances unattributed | Solved (accepted residual) — `hyperv-host-companion` |
| 4 | No guest filesystem used % visible | Solved (pending fleet-wide validation) — Tier 1.5, mechanism-validated on a real nested-Hyper-V test; ships `disabled` until fleet go/no-go |
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
