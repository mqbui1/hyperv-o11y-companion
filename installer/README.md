# installer/

WiX v4 source producing a single MSI with two independently selectable
features:

| Feature | Installs | Deploy on |
|---|---|---|
| `ScvmmPollerFeature` | `hyperv-scvmm-poller` service + `scvmm-poller.yaml` | Central SCVMM console box only (e.g. `SCVMM-CONSOLE-01`) — one instance for the whole estate |
| `HostCompanionFeature` | `hyperv-host-companion` service + `host-companion.yaml` | Every physical Hyper-V host — one instance per host |

Both are registered as native Windows Services (`ServiceInstall`/
`ServiceControl`, `Start="auto"`), so they start on boot and stop cleanly on
uninstall — no Task Scheduler entries. This is only half the solution: the
Splunk OTel Collector (Tier 1, host-side Perfmon/event-log collection) is a
**separate** install per Hyper-V host, not part of this MSI — see step 5
below.

## 1. Build prerequisites

- Go toolchain, with cross-compile support for `GOOS=windows GOARCH=amd64`
  (build machine can be macOS/Linux/Windows — the build script sets
  `GOOS`/`GOARCH` itself).
- WiX v4 CLI: `dotnet tool install --global wix` (requires the .NET SDK).
- Run from a clone of this repo.

## 2. Build the MSI

```powershell
cd installer
.\build.ps1
```

What `build.ps1` actually does, in order:

1. Wipes and recreates `installer\staging\{bin,config}`.
2. Cross-compiles both binaries with `GOOS=windows GOARCH=amd64`:
   `go build -o staging\bin\scvmm-poller.exe ./cmd/scvmm-poller` and the same
   for `host-companion.exe`.
3. Copies `config\scvmm-poller.yaml` and `config\host-companion.yaml`
   (the example configs shipped in this repo) into `staging\config`.
4. Runs `wix build installer\main.wxs -o installer\out\HyperVO11yCompanion.msi`.

Output: `installer\out\HyperVO11yCompanion.msi`.

## 3. Before shipping to a customer — regenerate GUIDs

`installer\main.wxs` ships with **placeholder GUIDs** that are safe to build
and test with, but must never be reused for a real customer install:

- `Package/@UpgradeCode` (one value, near the top of the file)
- `Component/@Guid` on all six `Component` elements (`ScvmmPollerBinary`,
  `ScvmmPollerConfig`, `ScvmmPollerService`, `HostCompanionBinary`,
  `HostCompanionConfig`, `HostCompanionService`)

Generate fresh GUIDs (e.g. `[guid]::NewGuid()` in PowerShell) and replace each
placeholder before building the MSI you intend to hand to the customer.
Skipping this risks GUID collisions with any other WiX-built product on the
same machine or across future upgrades.

## 4. Install

Run on the appropriate box, with an elevated (Administrator) PowerShell
session:

```powershell
# Central SCVMM console box: SCVMM poller only
msiexec /i HyperVO11yCompanion.msi ADDLOCAL=ScvmmPollerFeature /qn

# Every physical Hyper-V host: host companion only
msiexec /i HyperVO11yCompanion.msi ADDLOCAL=HostCompanionFeature /qn
```

This installs to `C:\Program Files\Splunk\HyperVO11yCompanion\` (`bin\` and
`config\` subfolders) and registers the relevant Windows Service(s)
(`hyperv-scvmm-poller`, `hyperv-host-companion`), set to `Start=auto`. Neither
service starts running usefully until its config and credentials (steps
6–8 below) are in place — the service will start on boot but fail to reach
SCVMM/Splunk without them.

Config files (`config\scvmm-poller.yaml`, `config\host-companion.yaml`) are
installed with `NeverOverwrite="yes"` — re-running the installer (e.g. for an
upgrade) will not clobber an operator's already-tuned config. Edit the config
and restart the relevant service (`Restart-Service hyperv-scvmm-poller` /
`Restart-Service hyperv-host-companion`) to pick up changes; neither service
currently watches its config file for live reload.

## 5. Install the Splunk OTel Collector (Tier 1) — separate from this MSI

The MSI above only covers the two companion services (Tier 0 and the Tier 1
disk-metrics companion). The core Perfmon/event-log collection (Tier 1)
requires installing the **Splunk Distribution of the OpenTelemetry Collector
for Windows** separately, on **every** Hyper-V host — typically pushed via
whatever the customer already uses for host-level software (GPO/SCCM).

On each Hyper-V host, after installing the Splunk OTel Collector:

1. Replace its default config with `otel-collector/hypervisor-host-config.yaml`
   from this repo.
2. Set the collector's environment variables:
   - `SPLUNK_ACCESS_TOKEN` — the ingest token (a different token from the org
     token used for Terraform in step 9)
   - `SPLUNK_REALM`
3. Restart the `splunk-otel-collector` service.

This customer has explicitly ruled out deploying any collector inside guest
VMs, opt-in or otherwise (see `docs/architecture.md`).

## 6. Configure `scvmm-poller.yaml` (console box)

Edit `C:\Program Files\Splunk\HyperVO11yCompanion\config\scvmm-poller.yaml`:

```yaml
scvmm:
  server: "vmm01.example.com"        # customer's real SCVMM server FQDN
  cluster: "HYPERV-CLUSTER-01"       # scope both metrics + guest_os to this cluster; empty = whole estate (not recommended)
  credential_name: "hyperv-o11y/scvmm"

splunk:
  realm: "us1"                       # customer's real Splunk Observability Cloud realm
  credential_name: "hyperv-o11y/splunk-token"

otlp:
  endpoint: "http://localhost:4319"  # a dedicated OTLP receiver port on whatever collector this box points at
  insecure: true

metrics:
  interval: 60s
  include_vm_storage_network: true
  include_vm_vhd: true
  include_vm_perf: false             # off by default — Tier 1 already reports per-VM CPU/mem at 60s vs SCVMM's ~9min

guest_os:
  interval: 1h
  secure_boot_fallback: false        # needs cross-domain remote rights to hosts; enable once validated
  name_heuristic: true
  linux_name_markers: [dkp, nkp, ocp, k8s, kube, kub, rhel, rhl, centos, ubuntu, debian, suse, rocky, alma, fedora, linux, minio, harbor, zscaler]

host_include:                        # empty = all SCVMM hosts in scope; explicit allowlist mirrors today's -HostInclude
  - "HYPERV-HOST-01*"
  - "HYPERV-HOST-02*"
  - "HYPERV-HOST-03*"
```

Field-by-field:

| Field | What to set it to |
|---|---|
| `scvmm.server` | The customer's real SCVMM management server FQDN. |
| `scvmm.cluster` | The SCVMM cluster name to poll. Leave empty only if the whole SCVMM estate should be in scope (not recommended for a phased rollout). |
| `scvmm.credential_name` | Must match the name used in the `cmdkey` command in step 7 below. |
| `splunk.realm` | The customer's Splunk Observability Cloud realm (e.g. `us1`). |
| `splunk.credential_name` | Must match the name used in the `cmdkey` command in step 7 below. |
| `otlp.endpoint` | Wherever this box's OTLP receiver is — either a dedicated port on a local/host-local Splunk OTel Collector, or (if this console box is not itself running a collector) an OTLP endpoint reachable from it. |
| `metrics.interval` | How often to poll SCVMM for VM/host power-state metrics. |
| `metrics.include_vm_perf` | Leave `false` unless Tier 1's per-VM CPU/mem coverage is unavailable — SCVMM's own perf polling interval (~9 min) is much coarser. |
| `guest_os.interval` | How often to re-poll and write the `guest_os` dimension property. Longer is fine — this is a slowly-changing property, not a live metric. |
| `guest_os.secure_boot_fallback` | Leave `false` until the cross-domain remote-rights requirement is validated for the customer's environment. |
| `host_include` | Explicit allowlist of SCVMM host names/globs to scope polling to. Empty means every host SCVMM knows about — use an explicit list for a phased/pilot rollout. |

## 7. Configure `host-companion.yaml` (every Hyper-V host)

Edit `C:\Program Files\Splunk\HyperVO11yCompanion\config\host-companion.yaml`
on **each** Hyper-V host:

```yaml
otlp:
  endpoint: "http://localhost:4318"  # host-local Splunk OTel Collector; no credential needed here
  insecure: true

disk_map:
  build_interval: 1h
  build_timeout: 300s   # matches build-hyperv-vm-disk-map.ps1's -TimeoutSec 300; on timeout, keep the previous in-memory map

disk_metrics:
  sample_interval: 60s
  latency_scale: 1000.0  # seconds -> ms; empirically confirmed, see docs/known-gaps-remediation.md gap #5

guest_probe:   # Tier 1.5 — leave disabled until go/no-go, see docs/phase3-guest-probe-plan.md
  enabled: false
  vm_include: []
  sample_interval: 5m
  sample_timeout: 30s
  credential_name: "hyperv-o11y/guest-probe"
```

`otlp.endpoint` should almost always stay `http://localhost:4318` — this
service talks to the same host's own Splunk OTel Collector (installed in step
5), which already holds the upstream Splunk credential via its `signalfx`
exporter config. No Splunk credential is needed for `host-companion` itself.

Leave `guest_probe.enabled: false` (the shipped default) unless doing a
supervised Tier 1.5 pilot — see step 10 below. Do not change any other field
here on a per-host basis unless a specific host's disk topology requires a
longer `disk_map.build_timeout`.

## 8. Provision credentials via Windows Credential Manager

Neither service reads secrets from disk — both read named credentials from
Windows Credential Manager at startup (`internal/creds`), readable only by
the account the service runs as.

On the **SCVMM console box**, run (elevated PowerShell, as the account the
`hyperv-scvmm-poller` service will run as):

```powershell
cmdkey /generic:hyperv-o11y/scvmm       /user:example.com\svc-hyperv-o11y /pass:<SCVMM-read-only-account-password>
cmdkey /generic:hyperv-o11y/splunk-token /user:x-sf-token                /pass:<splunk-ingest-access-token>
```

- The SCVMM account only needs read rights (`Get-SCVMHost`, `Get-SCVirtualMachine`) — do not provision a privileged account.
- The Splunk credential's "username" field is unused by the dimension API and can be any placeholder (e.g. `x-sf-token`); the password field is the real ingest access token.
- The credential names (`hyperv-o11y/scvmm`, `hyperv-o11y/splunk-token`) must exactly match `scvmm.credential_name` / `splunk.credential_name` in `scvmm-poller.yaml`.

`hyperv-host-companion` needs **no credentials at all** in its default
(guest-probe-disabled) configuration — it only queries the local Hyper-V host
directly and exports to the local collector. Only if/when a Tier 1.5 pilot is
approved (step 10) does a guest-probe credential need to be provisioned.

## 9. One-time: provision dashboards + detectors (Terraform)

Separate from both the MSI and the collector — run once, from any machine
with Terraform installed, against the customer's Splunk org:

```powershell
cd terraform
cp terraform.tfvars.example terraform.tfvars   # fill in splunk_access_token + splunk_realm
terraform init
terraform apply
```

This requires an **org token** (admin scope) — a different token from the
ingest token used in step 5/8. See `docs/capabilities-and-metrics.md`'s
"What's already visualized" section for the full chart/detector breakdown
this provisions, and `docs/deployment-guide.md` for the full test/verify
plan after this step.

## 10. Optional: Tier 1.5 pilot enablement (guest filesystem/memory metrics)

Tier 1.5 (`guest_probe` in `host-companion.yaml`) closes gaps #2 and #4
without deploying anything inside the guest, but ships disabled pending
fleet-wide go/no-go per `docs/phase3-guest-probe-plan.md`. To run a small,
supervised pilot only (do not enable fleet-wide):

1. Provision a **guest-local** (not domain-admin, not host-level) read-only
   account valid inside the pilot guests, then on each pilot host:
   ```powershell
   cmdkey /generic:hyperv-o11y/guest-probe /user:<guest-local-user> /pass:<secret>
   ```
   Confirm `Get-VM <name> | Select IntegrationServicesState` reports `Up to
   date` for each pilot VM first.
2. On each pilot host's `host-companion.yaml`:
   ```yaml
   guest_probe:
     enabled: true
     vm_include: ["PilotVM01", "PilotVM02"]   # explicit names/globs; empty = nothing probed
     sample_interval: 5m
     sample_timeout: 30s
     credential_name: "hyperv-o11y/guest-probe"
   ```
3. `Restart-Service hyperv-host-companion` and confirm
   `vm.guest.filesystem.used_percent` / `vm.guest.memory.used_percent` appear
   on the pilot VMs' entities within one `sample_interval`.

Full details, including the three go/no-go criteria to validate during the
pilot, are in `docs/deployment-guide.md` and `docs/phase3-guest-probe-plan.md`.

## 11. Post-install verification

- `Get-Service hyperv-scvmm-poller`, `Get-Service hyperv-host-companion`,
  `Get-Service splunk-otel-collector` — all `Running`.
- In Splunk Infrastructure Monitoring, confirm hosts tagged
  `host.type=hypervisor` and `host.type=hypervisor_managed_vm` appear within
  a few minutes.
- Open the "Hyper-V: Hypervisor Overview" and "Hyper-V: VM Detail" dashboards
  and confirm charts populate — no blank panels.
- See `docs/deployment-guide.md`'s "Test / verify plan" for the full
  gap-by-gap checklist.

## Uninstall / rollback

```powershell
msiexec /x HyperVO11yCompanion.msi /qn
```

Uninstalling stops and removes the relevant Windows Service(s) cleanly — no
data-migration step, since neither service holds state the uninstall needs to
preserve. See `docs/parity-testing-and-cutover.md` for the shadow → diff →
cutover plan if replacing existing PowerShell scripts/scheduled tasks with
these services, including the recommended rollback path (re-enable the old
Scheduled Task rather than reinstalling the service).
