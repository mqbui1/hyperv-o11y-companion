# Phase 3 — Tier 1.5 Guest Probe Absorption Plan

Status: **not started**. Tier 1.5 (PowerShell Direct guest probe) is
currently a documented concept in the five-tier architecture, not running
code — there is no existing probe implementation anywhere in this
engagement to port. This document is the design for what Phase 3 will build
once the underlying PowerShell Direct POC reaches a go decision, so that
`host-companion` doesn't need a third rewrite later.

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

## Planned shape

- `internal/guestprobe/probe.go` — `SampleFilesystem(ctx, vmID string,
  cred creds.Credential) ([]FilesystemSample, error)`, shelling to
  `Invoke-Command -VMId <vmID> -Credential ... -ScriptBlock { Get-Volume |
  Select DriveLetter, Size, SizeRemaining }` and parsing JSON, same
  `powershell.exe -NoProfile -Command "... | ConvertTo-Json"` pattern as
  every other adapter in this repo.
- Credential: a guest-local read-only account, read from Windows Credential
  Manager via the existing `internal/creds` package (one more named
  credential, e.g. `guest-probe-cred`) — **not** a per-VM credential list;
  the plan assumes one shared guest-local account provisioned fleet-wide
  (via the customer's existing guest-VM provisioning process), matching how
  gap #4's opt-in Tier 2 subset is scoped today.
- New config section in `host-companion.yaml`: `guest_probe.enabled`,
  `guest_probe.vm_include` (opt-in list, mirroring Tier 2's curated-subset
  framing — this is not a fleet-wide default any more than Tier 2 is),
  `guest_probe.sample_interval`.
- New metric: `vm.guest.filesystem.used_percent`, tagged with the same
  `vm.name` used everywhere else in this repo, so it lands on the same
  Splunk Observability Cloud entity as the VM's Tier 1 metrics without any
  additional correlation work.

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
  needing in-guest filesystem visibility still require Tier 2.
- Any metric beyond guest filesystem used % without a separate ask — Tier
  1.5 is scoped narrowly to close gap #4 for VMs that don't warrant Tier 2,
  not to become a general-purpose in-guest collector.
