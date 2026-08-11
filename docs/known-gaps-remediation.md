# Known Gaps and Remediation

Source: a real customer POC test summary (`HV-CLUSTER-01` cluster, hosts
`HV-HOST-01/02/03`), "Known gaps and open items" section. This maps each
finding to a concrete fix in this repo, or to an explicit non-goal with a
recommended workaround.

## 1. Availability / power-state monitoring — SOLVED (implemented, not yet live-tested end-to-end)
No VM/host "down" detection exists in Tier 1 (`hypervisor-host-config.yaml`) —
Perfmon counters only report state for VMs that are running; a powered-off VM
simply stops emitting data, which is indistinguishable from "collector
briefly missed a scrape."

**Fix (implemented):** this is Tier 0 (SCVMM), not Perfmon. The customer's
`collect-scvmm-metrics.ps1` originally emitted `hyperv_vm_up`/`hyperv_host_up`
(0/1 gauges) sourced from SCVMM's PowerState/OverallState, tagged with the
same `vm.name`/`host.name` resource attributes this repo standardizes on, via
the same `signalfx` exporter pattern used in `hypervisor-host-config.yaml`.
This logic has been ported into `hyperv-scvmm-poller`'s `pollMetrics` loop
(`hyperv-o11y-companion` repo, `cmd/scvmm-poller/main.go`) — code-complete,
but (unlike `hyperv-host-companion`, which was live-tested against a real
Hyper-V host this round) not yet run end-to-end against a real SCVMM server;
that live-test pass is the next priority for this service. This repo still
does not reimplement SCVMM's own PowerShell cmdlets; `internal/scvmm` still
shells out to `Get-SCVMHost`/`Get-SCVirtualMachine` under the hood.

## 2. Static-memory VMs invisible to memory-pressure alerting — SOLVED (pending fleet-wide validation)
By design: "Current Pressure" (`Hyper-V Dynamic Memory VM` object) only
exists for VMs with Dynamic Memory enabled. Static-memory VMs never populate
this counter, so `vm_memory_pressure_high` in `detectors.tf` silently never
fires for them — not a bug, a coverage gap.

**Status: implemented in `hyperv-host-companion`, disabled by default.**
Tier 1.5 (`internal/guestprobe.Sample`) queries `Win32_OperatingSystem`
inside the guest (`TotalVisibleMemorySize`/`FreePhysicalMemory`, both in
KB) in the same `Invoke-Command` session used for gap #4, and exports
`vm.guest.memory.used_percent`. This is the guest's own OS-reported memory
usage, not Hyper-V's Dynamic-Memory-only "Current Pressure" counter, so it
works for static-memory VMs — the whole point of this gap. Mechanism
validated end-to-end against a real nested-Hyper-V guest (see
`docs/phase3-guest-probe-plan.md`). Same caveat as gap #4: ships with
`guest_probe.enabled: false`, and should stay off until fleet-wide go/no-go
validation (session latency/load at scale, real-fleet Integration Services
coverage, shared-credential security review) is complete.

## 3. ~19–23% of VHD instances unattributed (fleet match rate 77–81%) — SOLVED
`Get-VMHardDiskDrive` (used by the customer's `build-hyperv-vm-disk-map.ps1`)
doesn't return DVD/ISO-mounted drives or pass-through disks, so a fifth of
the `Hyper-V Virtual Storage Device` Perfmon instances can't be mapped back
to a VM name by path alone.

**Fix (implemented, option (b) below):** `transform/vm_name` in
`hypervisor-host-config.yaml` handles the common
`...\Virtual Hard Disks\VMName.vhdx` path pattern and strips the Perfmon `#N`
duplicate suffix (gap #6), but — as originally noted here — intentionally
does not guess VM names for pass-through/ISO instances. The customer's
`build-hyperv-vm-disk-map.ps1`/`collect-hyperv-vm-disk.ps1` pair resolves
live storage-device counter instances against an hourly path→VM cache
instead, achieving a measured 77–81% fleet match rate; the remaining
~19–23% (DVD/ISO/pass-through disks) is a deliberate, accepted exclusion —
there's no reliable signal in the raw instance string for those, not a
defect. This logic is being ported into `hyperv-host-companion`
(`hyperv-o11y-companion` repo, Phase 2, in progress) as an in-process disk
map shared between a builder and sampler goroutine, replacing the two
scripts' JSON-file handoff.

## 4. No guest filesystem used % visible — SOLVED (pending fleet-wide validation)
Confirmed architectural limitation: `host.disk.free_space` / Hyper-V's
`Hyper-V Virtual Storage Device` counters describe host-visible virtual disk
*files*, not what's actually used inside the guest's filesystem.

**Status: implemented in `hyperv-host-companion`, disabled by default.**
Tier 1.5 (PowerShell Direct guest probe, `internal/guestprobe`) is real code: a
third ticker in `cmd/host-companion/main.go` calls `Invoke-Command -VMId`
over VMBus for an opt-in `guest_probe.vm_include` subset and exports
`vm.guest.filesystem.used_percent`. It ships with `guest_probe.enabled:
false` and should stay off until the go/no-go criteria in
`docs/phase3-guest-probe-plan.md` are validated against the real fleet
(session latency/load at scale, guest Integration Services coverage, and
whether a single shared guest-local credential is acceptable). Mechanism
validated end-to-end against a real nested-Hyper-V guest (16.30% filesystem
used, matched hand-computed expected value) — see
`docs/phase3-guest-probe-plan.md`.

`otel-collector/hypervisor-host-config.yaml` now has an `otlp` receiver and
a `metrics/vm_companion` pipeline to receive and correctly tag
`vm.disk.*`/`vm.guest.filesystem.used_percent` from `hyperv-host-companion`
(previously this receiver didn't exist at all — Phase 2's disk metrics had
nowhere to land). `terraform/dashboards.tf`'s VM Detail dashboard and a new
`vm_guest_filesystem_used_high` detector (ships `disabled = true`) in
`terraform/detectors.tf` are wired to the real metric name.

## 5. Disk latency unit unconfirmed — SOLVED
`vm.storage.latency` (from the `Hyper-V Virtual Storage Device` object's
"Latency" counter) is documented by Microsoft as 100ns ticks, but this had
not been independently verified against a raw counter value in a live
environment.

**Fix (implemented):** confirmed via the customer's own real-fleet empirical
analysis — a live multi-thousand-series sample (from
`collect-hyperv-vm-disk.ps1`'s embedded comments) showed the counter reports
raw **seconds**, not 100ns ticks (p95≈0.0038s/3.8ms, p99≈0.0086s/8.6ms,
max≈0.0348s/34.8ms — textbook SAN/flash latency; a 100ns-tick reading would
round to near-zero, and a milliseconds reading would be physically
impossible at that sustained scale). `hypervisor-host-config.yaml` now
applies a ×1000 (seconds→ms) correction via
`transform/vm_storage_latency_scale` before export, and
`detectors.tf`'s `vm_storage_latency_high` detector is enabled with its
threshold expressed in ms (20ms).

## 6. Malformed vm.name values from Perfmon duplicate-instance suffixing
A handful of instances observed with a trailing `#1` (the cluster has several
dozen duplicate-named VMs across its hosts, and Perfmon disambiguates same-named instances by
appending `#N`). The prior `transform/vm_name` logic didn't strip this, so
those VMs' metrics landed under a malformed `vm.name` like `WebServer#1`
instead of `WebServer`.

**Fix:** added a final OTTL statement to `transform/vm_name` in
`hypervisor-host-config.yaml`:
```
set(attributes["vm.name"], Split(attributes["vm.name"], "#")[0]) where attributes["vm.name"] != nil and IsMatch(attributes["vm.name"], ".*#[0-9]+$")
```
This runs last (after all object-specific extraction), since the `#N` suffix
can appear regardless of which Perfmon object the instance string came from.
Note this does not solve name *collisions* — if a customer has many VMs with
duplicate names, this correctly strips the disambiguator but the VMs will
still land on the same `host.name` entity in Splunk Observability Cloud
(their metrics will merge). That's a genuine naming-hygiene problem on the
customer's Hyper-V estate, not something an OTel processor can fix — flagged
as an out-of-band recommendation (rename duplicate VMs) rather than a config
workaround.

## 7. ~20% of VMs emit no network series — SOLVED for a from-scratch deployment (unconfirmed whether it fully resolves the original POC's cluster)
Originally documented as an unconfirmed root cause (candidates: VMs with no
vNIC attached, VMs powered off during the collection window, or a naming edge
case in the `Hyper-V Virtual Network Adapter` instance string not covered by
`transform/vm_name`).

**Real root cause found (real-hardware validation, Azure nested Hyper-V):**
the config was requesting a counter that doesn't exist on either the
`Hyper-V Virtual Network Adapter` or `Hyper-V Virtual Switch` Perfmon
objects. Both receiver blocks in `hypervisor-host-config.yaml` (metrics
`vm.net.bytes_total` and `hyperv.vswitch.bytes_total`) used
`{ name: "Bytes Total/sec", ... }` — that exact counter name only exists on
the standard Windows `Network Interface` object (used correctly in
`windowsperfcounters/host` for `host.net.bytes_total`). Confirmed via a live
`Get-Counter -ListSet "Hyper-V Virtual Switch"` / `"Hyper-V Virtual Network
Adapter" | ... .PathsWithInstances` collection: live instances existed
(`Default Switch`, per-VM vNIC GUIDs) but the only real counter names on
those objects are `Bytes/sec`, `Bytes Sent/sec`, `Bytes Received/sec` — never
`Bytes Total/sec`. This meant `vm.net.bytes_total` and
`hyperv.vswitch.bytes_total` were **entirely** non-functional (0% match, not
~20%) on every host, not just partially missing on a subset of VMs.

**Fix:** corrected both counter names to `Bytes/sec` in
`hypervisor-host-config.yaml`. Verified after the fix + collector restart
that both metrics now populate in Splunk Observability Cloud Metrics Finder.

**Still open:** this fix explains a *total* outage of these two metrics on
a from-scratch deployment. It does not by itself explain the *original* POC
finding of "~80% match rate" on a cluster that presumably already had some
network data flowing under a different (possibly already-correct) config
version — if that gap resurfaces after applying this fix on the customer's
own environment, fall back to the original candidates above (no vNIC
attached, VM powered off during collection, or an uncovered instance-string
edge case) and cross-reference against `Get-VMNetworkAdapter` output. Do NOT
build a "no network data" detector on `vm.net.bytes_total` until any
residual gap is spot-checked — a naive "series stopped/never existed"
detector would false-positive on every VM that legitimately has no vNIC.

## 8. guest_os accuracy issues (a meaningful number of unknown/untagged VMs; heuristic Linux tagging; secure-boot fallback needs remote rights) — SOLVED (implemented, not yet live-tested end-to-end)
This is entirely inside the customer's `enrich-vm-guest-os.ps1` script, not
in this repo's OTel/Terraform layer — this accelerator doesn't ingest or
re-derive `guest_os` itself.

**Fix (implemented, in the customer's script):** `enrich-vm-guest-os.ps1`
classifies `guest_os` (windows/linux/other/unknown) from SCVMM's
`OperatingSystem` field first, falls back to a Secure Boot template check for
still-Unknown Gen2 VMs (UEFI CA/OpenSourceShielded ⇒ Linux), and an opt-in
naming heuristic as a last resort — writing the result as a `guest_os`
dimension property via the SignalFx metadata API (GET→merge→PUT), keyed by
`vm.name`/the `vm` dimension so it joins onto every metric already carrying
that value, at zero additional MTS cost. Ambiguous duplicate VM names are
skipped, not clobbered (consistent with gap #6). This logic has been ported
into `hyperv-scvmm-poller`'s `pollGuestOS` loop (`hyperv-o11y-companion` repo,
`cmd/scvmm-poller/main.go`, using `internal/guestos` + `internal/metadata`) —
code-complete, but not yet run end-to-end against a real SCVMM server (same
caveat as gap #1 above).

## 9. VMMS load issues driven by failed live migrations (Event ID 21026)
Confirmed root cause on `HV-HOST-01`; `HV-HOST-03` needed a
`disk_map_build_timeout_sec: 900` override to work around the resulting
slowness.

**Fix:** added a `count` connector (`count/migration_failures`) in
`hypervisor-host-config.yaml` that converts Event ID 21026 occurrences on the
`Microsoft-Windows-Hyper-V-VMMS-Admin` channel into a metric
(`hyperv.vmms.migration_failures`), plus a new `metrics/migration_failures`
pipeline and `vmms_migration_failures` detector in `detectors.tf`. This makes
the root cause directly alertable instead of only visible after the fact via
`disk_map_build_timeout_sec` workarounds. **Caveat:** the connector's
condition (`attributes["winlog.event_id"] == 21026`) assumes the
`windowseventlogreceiver`'s attribute key — verify this against a raw
ingested event for the collector version actually deployed, since this
attribute naming has changed across contrib releases.

## 10. environment=poc → production cutover
Already validated as low-risk in the POC via the `host.type == "hypervisor"`
/ `"hypervisor_managed_vm"` filter design used throughout
`hypervisor-host-config.yaml`, `dashboards.tf`, and `detectors.tf` — those
filters are environment-agnostic. No repo change needed; just point
`SPLUNK_REALM`/`SPLUNK_ACCESS_TOKEN` at the production org token and roll the
same config out to the remaining hosts.

## Additional findings from real-hardware validation (nested Hyper-V, Azure)

Two corrections/refinements to gap #6, found by running
`docs/nested-hyperv-azure-test-plan.md` against a real Windows Server 2025
Datacenter host with two identically-named VMs (`vm-alpha` x2):

**CPU counter — validated as originally documented, with a testing gotcha.**
`Get-Counter -ListSet "Hyper-V Hypervisor Virtual Processor" | ...
.PathsWithInstances` returns identical, non-disambiguated instance strings
for both duplicate VMs — this initially looked like Windows wasn't
suffixing at all. That was a false signal from the query method, not real
behavior: a live wildcard collection (`Get-Counter
'\Hyper-V Hypervisor Virtual Processor(*)\% Guest Run Time'`) correctly
shows `vm-alpha:hv vp 0` and `vm-alpha:hv vp 0#1` as distinct instances. The
existing `#[0-9]+$` strip in `transform/vm_name` is correct as designed.
**Lesson: always validate Perfmon instance-string assumptions with a live
`Get-Counter` collection, not `-ListSet`.**

**Network Adapter counter — uses a different, non-fixable mechanism, same
practical outcome.** Real instance format is
`VMName_Network Adapter_<VM-GUID>--<NIC-GUID>` — confirmed via live
collection. Windows disambiguates duplicate-named VMs here via the VM's own
object GUID, never a `#N` suffix, so the strip statement is simply a no-op
for this object. However, `transform/vm_name`'s existing extraction (split
on `_Network Adapter`) discards that GUID, so duplicate-named VMs still
collapse to the same `vm.name` here too — the same practical collision as
the CPU case, just arrived at differently. **This cannot be fixed by
preserving the GUID as a separate resource attribute** — CPU, memory, and
storage instance strings never expose a VM GUID, so doing that would split a
single (non-duplicate-named) VM's Network metrics onto a different Splunk
Observability Cloud entity than its CPU/memory/storage metrics, which is a
worse regression than the collision it would fix. The underlying issue
remains what gap #6 already states: duplicate VM names on a Hyper-V host are
a customer naming-hygiene problem, not something fixable in collector
config, regardless of which raw mechanism (suffix or GUID) Windows uses per
counter object.

**Storage counter — new caveat, not previously covered by gap #3/#6.**
The raw instance string is the VHDX's full path, not just its filename (e.g.
`C:\VMs\Virtual Hard Disks-vm-alpha.vhdx` vs. `C:\VMs2\Virtual Hard
Disks-vm-alpha.vhdx` for two different VMs' disks — confirmed these do NOT
collide with each other because the directory differs). But
`transform/vm_name`'s extraction only keeps the filename-derived name, which
means it silently trusts that the VHDX filename matches its owning VM's
name — Hyper-V's own default when a VM's disk is created via `New-VM`
without a custom path. If a customer's storage doesn't follow that
convention (cloned templates, renamed disks, reused VHDX files across VMs),
this **misattributes** storage metrics to the wrong VM name, rather than
just failing to attribute them (gap #3's failure mode). There's no reliable
signal in the raw counter to detect or correct this — it's a real,
unresolved limitation, not a bug. See `docs/limitations.md`, item #10.
