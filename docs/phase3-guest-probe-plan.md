# Phase 3 — Tier 1.5 Guest Probe Absorption Plan

Status: **implemented, disabled by default pending go/no-go**. Tier 1.5
(PowerShell Direct guest probe) is now a third ticker in
`cmd/host-companion/main.go`'s `runService` loop
(`internal/guestprobe/probe.go`), gated by `guest_probe.enabled: false` in
`config/host-companion.yaml` — the code exists and can be exercised, but
should not be flipped on in production until the go/no-go criteria below
are validated against the real fleet.

## What Tier 1.5 is and why it exists

`Invoke-Command -VMId <guid> -Credential ... -ScriptBlock { ... }` lets a
process running **on the Hyper-V host** run PowerShell **inside a guest VM**
over the VMBus (Hyper-V's host↔guest integration-component channel) — no
guest network stack, no guest-facing firewall rule, no in-guest agent
process required. Requirements: guest Integration Services running, guest
OS supports PowerShell Direct (Windows guests only — no Linux support, this
only closes the gap for Windows-guest VMs), and a credential valid inside
the guest.

This sits between Tier 1 (host-visible only, e.g. VHD file size) and Tier 2
(full in-guest OTel Collector, billed as a separate host) as a way to get
a few specific in-guest values — most importantly guest filesystem used %
(gap #4) — for VMs that don't justify Tier 2's per-VM billing cost, without
deploying anything inside the guest at all.

## Why this belongs in `host-companion`, not a new binary

`host-companion` already runs on every Hyper-V host and already has:
- The disk-map builder's `Get-VM` enumeration (`internal/hyperv.BuildDiskMap`)
  — the guest probe needs the same list of running VM IDs to iterate.
- The PowerShell-shell-out adapter pattern (`internal/hyperv`) — the probe
  is one more narrow adapter of the same shape, not a new architectural
  pattern.
- An existing OTLP export path (`internal/metricsexport`) to the host-local
  Splunk OTel Collector.

Phase 3 adds a third ticker to `cmd/host-companion/main.go`'s `runService`
loop, not a new service.

## Implemented shape

- `internal/guestprobe/probe.go` — `SampleFilesystem(ctx, vmID string,
  cred creds.Credential) ([]FilesystemSample, error)`, shelling to
  `Invoke-Command -VMId <vmID> -Credential ... -ScriptBlock { Get-Volume |
  Where DriveType -eq Fixed | Select DriveLetter, Size, SizeRemaining }`
  and parsing JSON, same `powershell.exe -NoProfile -Command "... |
  ConvertTo-Json"` pattern as every other adapter in this repo. The guest
  credential's password is passed via a process environment variable, not
  a command-line argument, so it never appears in a process listing.
- Credential: a guest-local read-only account, read from Windows Credential
  Manager via the existing `internal/creds` package
  (`guest_probe.credential_name`, default `hyperv-o11y/guest-probe`) —
  **not** a per-VM credential list; this assumes one shared guest-local
  account provisioned fleet-wide (via the customer's existing guest-VM
  provisioning process). Whether that single-shared-credential model is
  acceptable is go/no-go criterion #3 below, not yet resolved.
- Config section in `host-companion.yaml`: `guest_probe.enabled` (default
  `false`), `guest_probe.vm_include` (opt-in glob-pattern list against VM
  name, e.g. `["WebServer*"]` — empty means no VMs are probed even when
  enabled; there is no fleet-wide default), `guest_probe.sample_interval`
  (default 5m — PowerShell Direct sessions are heavier than local
  `Get-Counter`, so this samples less often than the disk sampler),
  `guest_probe.sample_timeout` (default 30s per VM, so one hung guest with
  stale Integration Services can't block the others).
- Metric: `vm.guest.filesystem.used_percent`, tagged with the same
  `vm.name` used everywhere else in this repo (plus `drive_letter`), so it
  lands on the same Splunk Observability Cloud entity as the VM's Tier 1
  metrics without any additional correlation work.
- VM enumeration is reused from the existing disk-map builder
  (`state.get().ByID`, populated by `internal/hyperv.BuildDiskMap`) — no
  separate `Get-VM` shell-out needed for the probe ticker itself.
- `otel-collector/hypervisor-host-config.yaml` now has an `otlp` receiver
  (`0.0.0.0:4317`/`4318`, matching `host-companion.yaml`'s default
  `otlp.endpoint`) and a `metrics/vm_companion` pipeline that promotes
  `vm.name` to a resource attribute and tags `host.type`/`host.name` the
  same way `metrics/vm` does for Tier 1's own metrics, so this metric lands
  on the same Splunk Observability Cloud entity — this receiver previously
  didn't exist, so even the existing (Phase 2) `vm.disk.*` metrics had
  nowhere to land before this change.

## Live migration safety (no explicit dedup logic needed)

Metrics are keyed by the `vm.name` dimension, not a host+VM composite key —
same as every other metric in this repo. `host-companion` only probes VMs
that appear in its **local** `Get-VM` enumeration (via the shared disk
map), and Hyper-V VM ownership is exclusive to one host at a time. So when
a VM live-migrates:

- The **source** host's next disk-map rebuild drops the VM from its local
  enumeration — its guest-probe ticker simply stops querying that VM ID
  (which is now invalid on that host anyway).
- The **destination** host's next disk-map rebuild picks the VM up and its
  guest-probe ticker starts querying it there.

There is no window where both hosts are actively probing the same VM
simultaneously in steady state, and no per-VM subscription/registration
object that needs explicit cleanup on migration — the dimension-keyed MTS
model in Splunk Observability Cloud just continues the same time series
under whichever host is currently reporting it. The only gap is the window
between migration completing and the next `disk_map.build_interval` tick on
each host (default 1h) — during that window the destination host won't yet
probe the VM. Shortening `disk_map.build_interval` narrows this window at
the cost of more frequent `Get-VM` shell-outs; not yet tuned against a real
migration-frequency profile.

## Mechanism validated against a real nested Hyper-V guest (2026-08-07)

`internal/creds.NewReader().Read()` + `internal/guestprobe.Sample()` were
run for real (not synthetic) against a genuine Windows Server 2022 Server
Core guest on a nested-virtualization Azure host — Integration Services
reporting `OK`/`Heartbeat`, guest created via unattended DISM apply +
`bcdboot` (no Convert-WindowsImage/PSGallery dependency needed). Confirmed:
Credential Manager round-trip resolved the stored `.\Administrator`
credential; `Invoke-Command -VMId` succeeded over VMBus with **no guest
network path at all** (the nested switch is Internal-only); JSON decoding
and `UsedPercent()` math were correct on real numbers (16.30% filesystem
used, 39.36% memory used on a VM with **Dynamic Memory explicitly
disabled** — i.e. exactly gap #2's static-memory case that Hyper-V's own
Current Pressure counter can't see).

This validates the mechanism works correctly end-to-end on one VM. It does
**not** satisfy the go/no-go criteria below, which are about fleet-scale
behavior (hundreds of concurrent sessions, real-fleet IC coverage, credential
model acceptability) — `guest_probe.enabled` should stay `false` until those
are separately validated.

## Go/no-go criteria (owned by the POC, not this repo)

Before Phase 3 implementation starts, the PowerShell Direct POC needs to
confirm:
1. Latency/load of `Invoke-Command -VMId` at fleet scale (hundreds of VMs
   per host in the worst case) — PowerShell Direct sessions are not free;
   confirm this doesn't compete meaningfully with the disk-map
   builder/sampler tickers already running.
2. Guest Integration Services coverage across the fleet's actual guest OS
   versions — if a meaningful fraction of guests don't have current
   Integration Services, Tier 1.5's coverage will have its own gap,
   analogous to gap #2/#4's Tier 1/Tier 2 coverage gaps.
3. Whether a single shared guest-local credential is acceptable from a
   security-review standpoint, or whether per-VM/per-domain credential
   handling is required — this materially changes `internal/creds` usage
   above.

## Non-goals for Phase 3

- Linux guest support — PowerShell Direct is Windows-guest-only; Linux VMs
  needing in-guest filesystem/memory visibility still require Tier 2.
- Any metric beyond guest filesystem used % (gap #4) and guest memory used
  % (gap #2) without a separate ask — Tier 1.5 is scoped to these two
  specific gaps for VMs that don't warrant Tier 2, not to become a
  general-purpose in-guest collector. Both are gathered in one
  `Invoke-Command` session per VM per cycle (`internal/guestprobe.Sample`),
  not two, to minimize PowerShell Direct session overhead at fleet scale.
