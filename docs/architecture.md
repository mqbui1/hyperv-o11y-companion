# Architecture

## Overview

Six-tier collection. What started as a two-tier host/guest split (mirroring
the pattern the vSphere navigator in Splunk Observability Cloud relies on)
grew four more tiers once real customer POC findings showed Perfmon alone
can't see VM power-state, live-migration failures, or (for most fleets)
in-guest process/app data at an affordable MTS cost. Each tier is a distinct
collection mechanism with a distinct blast radius — see
`docs/known-gaps-remediation.md` for exactly which gap each tier closes.

Tier 1.5 and Tier 1.6 both close gap #4 (guest filesystem used %), via two
different mechanisms with different guest-OS coverage: Tier 1.5
(PowerShell Direct) is Windows-guest-only but also covers gap #2 (guest
memory, static-memory VMs); Tier 1.6 (host-side VHDX read) is Linux- and
Windows-guest capable but filesystem-only, and never runs anything inside
the guest at all — not even a one-off command. They are independent
Windows Services (`internal/guestprobe` inside `cmd/host-companion`, vs.
`internal/guestfs` inside its own `cmd/guestfs-probe`), not alternates
wired into the same process, so a customer can run either, both, or
neither without one implementation affecting the other.

```
┌───────────────── Tier 0 — SCVMM console (hyperv-scvmm-poller) ─────────┐
│  hyperv-o11y-companion repo, cmd/scvmm-poller — Windows Service         │
│  Shells to SCVMM PowerShell module once per poll (no on-prem REST API) │
│   hyperv_vm_up / hyperv_host_up  -> gap #1 (power-state, SOLVED)       │
│   guest_os dimension property    -> gap #8 (guest OS accuracy, SOLVED) │
└──────────────────────────────┬───────────────────────────────────────┬─┘
                                │ OTLP metrics          SignalFx metadata │
                                │                        API (dimension) │
                                ▼                                        ▼
┌─────────────────────────── Tier 1 — every Hyper-V host ────────────────┐
│  Splunk OTel Collector (otel-collector/hypervisor-host-config.yaml)    │
│  deploy on EVERY host, no per-VM config needed                         │
│                                                                          │
│   windowsperfcounters/host        -> host.*        (host.type=hypervisor)
│   windowsperfcounters/hypervisor  -> hyperv.*       (host.type=hypervisor)
│   windowsperfcounters/vm          -> vm.*           (host.type=hypervisor_managed_vm)
│   windowseventlog/*               -> Hyper-V event logs
│   count/migration_failures        -> hyperv.vmms.migration_failures
│                                                                          │
├─────────────── Tier 1 companion — every Hyper-V host ──────────────────┤
│  hyperv-o11y-companion repo, cmd/host-companion — Windows Service       │
│  vm.disk.{latency,read_bytes_sec,write_bytes_sec} via in-memory disk    │
│  map (Get-VM | Get-VMHardDiskDrive), OTLP to the host-local Splunk      │
│  OTel Collector above  -> gap #3 (VHD attribution, SOLVED)              │
└──────────────────────────────┬─────────────────────────────────────┘
                                │ signalfx exporter
                                ▼
                  Splunk Observability Cloud
                  (ingest.$SPLUNK_REALM.signalfx.com)
                                ▲
              ┌─────────────────┴──────────────────┐
              │ Tier 1.5 — PowerShell Direct        │
              │ guest probe (implemented,           │
              │ disabled by default pending          │
              │ go/no-go — the only in-guest         │
              │ metrics path in this proposal;        │
              │ Windows guests only)                  │
              │ in-guest metrics with no guest       │
              │ network egress or agent required     │
              └──────────────────────────────────────┘
              ┌──────────────────────────────────────┐
              │ Tier 1.6 — VHDX host-side read         │
              │ (cmd/guestfs-probe, implemented,       │
              │ disabled by default) — Windows AND     │
              │ Linux (RHEL 7/8/9, GPT+XFS) guests,     │
              │ zero guest interaction of any kind —   │
              │ parses the guest's own VHDX file        │
              │ directly on the host                   │
              └──────────────────────────────────────┘
              ┌──────────────────────────────────────┐
              │ Future idea, not gap-driven, not built │
              │ Windows Event Forwarding — would       │
              │ centralize guest/host event logs       │
              │ without per-VM collector deployment    │
              └──────────────────────────────────────┘
```

## Tier-by-tier summary

| Tier | What it is | Where it runs | Gaps closed |
|---|---|---|---|
| 0 | `hyperv-scvmm-poller` (`hyperv-o11y-companion` repo) — Windows Service polling SCVMM | Central SCVMM console box (e.g. `SCVMM-CONSOLE-01`) | #1 (power-state, SOLVED), #8 (guest_os, SOLVED) |
| 1 | Splunk OTel Collector, `hypervisor-host-config.yaml` | Every Hyper-V host | Host/hypervisor/VM Perfmon metrics, VMMS migration-failure events (#9) |
| 1 (companion) | `hyperv-host-companion` (`hyperv-o11y-companion` repo) — Windows Service | Every Hyper-V host, alongside Tier 1's collector | #3 (VHD attribution, SOLVED) |
| 1.5 | PowerShell Direct guest probe (`internal/guestprobe`) | Host-initiated, no in-guest agent/network egress | #2/#4 for **Windows** guests (implemented, `guest_probe.enabled: false` by default — blocked on go/no-go decision, see `docs/phase3-guest-probe-plan.md`) |
| 1.6 | VHDX/GPT/filesystem-superblock host-side read (`internal/guestfs`, `cmd/guestfs-probe`) | Own Windows Service, every Hyper-V host; zero guest interaction of any kind | #4 for **Windows and Linux** guests (implemented and validated end-to-end against a real Linux guest, `guest_fs_probe.enabled: false` by default pending fleet pilot; v1 supports GPT+XFS only — see `docs/known-gaps-remediation.md` gap #4) |

**Future idea, not gap-driven, not built:** Windows Event Forwarding — a
native Windows mechanism that could centralize event-log visibility (host +
guest) without per-VM collector deployment. No code, no config in this repo.
Not part of this proposal, and no customer-POC gap requires it — unlike
every numbered tier above, it isn't tied to a specific finding.

## Resource attribute strategy

This is the load-bearing design decision in the repo — get this wrong and
dashboards/detectors (which filter on `host.type`) silently return nothing.

| Attribute | Set by | Value | Purpose |
|---|---|---|---|
| `host.type` | `resource/hypervisor_tag`, `resource/vm_tag` | `hypervisor` \| `hypervisor_managed_vm` | Every chart/detector filter in `terraform/dashboards.tf` and `terraform/detectors.tf` is keyed on this. Determines whether a metric is host-side or VM-side. |
| `host.name` | `resourcedetection` (Tier 1 host/hypervisor pipelines) or `transform/vm_hostname` (Tier 1 vm pipeline, copied from `vm.name`) | Hypervisor's own hostname, or the VM's name | The entity key — determines whether a metric lands as its own VM entity or merges onto the host entity. |
| `vm.name` | `transform/vm_name` (extracted from noisy Perfmon instance strings — see below) | Clean VM name | Intermediate attribute, promoted to `host.name` and used as the `groupbyattrs` key. |
| `hypervisor.host.name` | `resource/vm_tag` (`${env:COMPUTERNAME}`) | Parent hypervisor's hostname | Stamped onto every VM's metrics so the infra navigator can link VM -> parent host, independent of whether `host.name` correlation (above) also works. |
| `virtualization.system` | `resource/hypervisor_tag` | `hyperv` | Distinguishes from other virtualization platforms if this repo's output ever lands alongside vSphere/other hypervisor data in the same org. |

If VM names contain duplicates on the same host (see
`docs/known-gaps-remediation.md`, gap #6), correlation degrades: multiple
VMs' metrics will merge onto one entity. That's a Hyper-V-estate
naming-hygiene problem, not something the collector config can resolve.

## Why `vm.name` extraction is non-trivial

Windows Perfmon does not expose a clean "VM name" field — VM identity is
embedded inside instance strings whose format differs per counter object:

| Object | Example instance string | Extraction rule |
|---|---|---|
| `Hyper-V Hypervisor Virtual Processor` | `VMName:Hv VP 0` | split on `:`, take first segment |
| `Hyper-V Dynamic Memory VM` | `VMName` | already clean |
| `Hyper-V Virtual Network Adapter` | `VMName_Network Adapter_{VM-GUID}--{NIC-GUID}` | split on `_Network Adapter` (discards the GUID) |
| `Hyper-V Virtual Storage Device` | `...\Virtual Hard Disks\VMName.vhdx` (full path; filename must match VM name by convention) | split on `Virtual Hard Disks-`, then strip `.vhdx` |
| CPU, when duplicated | `VMName#1` | strip trailing `#[0-9]+` (must run last — see gap #6) |

Validated against a real Hyper-V host (nested virtualization, Azure — see
`docs/nested-hyperv-azure-test-plan.md`): the CPU object's `#N` duplicate
suffix only appears in a **live** `Get-Counter` collection, not in
`-ListSet .PathsWithInstances` output — the latter returns a static,
non-disambiguated path enumeration and will produce a false negative if used
to validate this. The Network Adapter object never uses a `#N` suffix at
all; it disambiguates via an embedded VM GUID instead, which the extraction
above discards, producing the same practical duplicate-name collision as
the CPU case via a different raw mechanism. See
`docs/known-gaps-remediation.md`, "Additional findings" section, for why
this can't be fixed without breaking cross-metric-type entity correlation.

This entire chain lives in `transform/vm_name` in
`hypervisor-host-config.yaml`, ordered so later, more-specific statements
override the generic first-pass copy. `filter/vm_noise` runs first to drop
non-VM instances (`Default Switch`, `.vmgs`, `.iso`) before extraction, and
`groupbyattrs/vm` runs after to promote the final `vm.name` from a datapoint
attribute to a resource attribute (making each VM its own entity).

## Event-to-metric conversion (VMMS migration failures)

Detectors in Splunk Observability Cloud alert on metric time series, not raw
log events. `hypervisor-host-config.yaml` uses the `count` connector
(`count/migration_failures`) to convert Event ID 21026 occurrences
(live-migration failures) on the `Microsoft-Windows-Hyper-V-VMMS-Admin`
channel into a `hyperv.vmms.migration_failures` metric, fed to its own
`metrics/migration_failures` pipeline and the `vmms_migration_failures`
detector in `terraform/detectors.tf`. This is the general pattern to follow
if other event-log channels need to become alertable — see
`docs/known-gaps-remediation.md`, gap #9.

## Dashboards and detectors (Terraform)

- `terraform/main.tf` — `signalfx` provider + `signalfx_dashboard_group.hyperv`
- `terraform/dashboards.tf` — two dashboards: "Hypervisor Overview" (Tier 1
  only, one row per host) and "VM Detail" (Tier 1 VM metrics; the guest
  filesystem chart points at Tier 1.5's real `vm.guest.filesystem.used_percent`
  metric — see gap #4)
- `terraform/detectors.tf` — VM health critical, hypervisor CPU high, VM
  memory pressure high, VM storage latency high (unit confirmed via
  real-fleet empirical analysis and enabled at a 20ms threshold — gap #5,
  SOLVED), guest filesystem used high (gap #4 — same metric name,
  producible by either Tier 1.5 or Tier 1.6; ships `disabled = true`,
  enable alongside whichever tier's `*.enabled` flag is turned on), VMMS
  migration failures

All chart/detector `program_text` filters on `host.type`, per the table
above — this is why getting the resource attribute strategy right matters
more than any individual metric mapping.

## What this repo deliberately does not do

- Reimplement Hyper-V inventory/power-state polling or SCVMM/disk-map
  scripting itself — that's Tier 0/Tier 1-companion's job, now consolidated
  into the `hyperv-o11y-companion` repo's two Windows Services
  (`hyperv-scvmm-poller`, `hyperv-host-companion`); this repo's job is to make
  that data (once emitted as OTLP/dimension-API writes with matching resource
  attributes) land on the same entities as everything else here.
- Deploy any collector inside guest VMs at all for this customer, per
  explicit customer decision (see gap #2/#4) — confirmed to apply to every
  guest OS, not just Windows. Tier 1.5 (PowerShell Direct guest probe) is
  the only in-guest-metrics path in this proposal, pending its own go/no-go
  decision; Tier 1.6 (VHDX host-side read) has no in-guest path at all to
  gate, since it never touches the guest.
- Attempt full content/navigator parity with the vSphere integration in one
  pass — this is a field accelerator (dashboards + detectors as code), not a
  claim of native-integration-equivalent coverage. See `docs/limitations.md`.
