# Limitations

This is a field-built accelerator, not a native Splunk Observability Cloud
integration. Read this before promising a customer parity with vSphere/
native-integration coverage.

## 1. No native Hyper-V integration exists in Splunk Observability Cloud

Everything in this repo is built on the generic `windowsperfcounters` and
`windowseventlogreceiver` OTel receivers — there is no Hyper-V-specific
receiver upstream, and Hyper-V is not on the Observability Cloud
infrastructure-monitoring roadmap as of this writing (not prioritized;
see internal solutioning notes). This repo is a stopgap, not a
forward-compatible guarantee — Microsoft can change counter names/objects
across Windows Server versions without notice, and this config has only
been validated against the counter set documented by Microsoft plus one
real customer POC cluster.

## 2. Virtualization isolation boundary: guest-OS-internal metrics are not visible from the hypervisor

This is not a Splunk or OTel limitation — it's fundamental to how
virtualization works, and would apply equally to vSphere without VMware
Tools reporting from inside the guest. From the host side (Tier 1,
`hypervisor-host-config.yaml`), you get **resource allocation as seen by the
hypervisor**:
- vCPU % time, not what's running inside the guest
- Assigned/pressure memory, not actual guest memory utilization
- Virtual disk **file** I/O (bytes/sec, latency), not guest filesystem used %
- vNIC throughput, not guest-level connection/socket state

You do NOT get, from Tier 1 alone:
- Guest filesystem free space (see `docs/known-gaps-remediation.md`, gap #4)
- Guest process/service state
- Guest application-level metrics
- Guest OS patch level, installed software, etc.

Getting any of the above would require Tier 2 (`guest-vm-config.yaml`),
deployed inside the guest. For this customer, that path is closed entirely
— see item 3 below. Tier 1.5 (PowerShell Direct guest probe,
`internal/guestprobe`) closes guest filesystem used % and guest memory used
% specifically — implemented and mechanism-validated against a real
nested-Hyper-V guest — but ships disabled pending fleet-wide go/no-go
validation; see `docs/known-gaps-remediation.md`, gaps #2/#4.

## 3. Tier 2 (in-guest collector) is ruled out entirely for this customer, not just fleet-wide

Every VM running an OTel Collector becomes a separately-billed host, which
was the original reason this repo scoped Tier 2 as opt-in/curated rather
than fleet-wide (in one real customer POC, ~1,478 physical/hypervisor hosts
hosted ~17,000 VMs — fleet-wide Tier 2 would have multiplied billable host
count roughly 12x). That billing math is now moot: **this customer has
separately, explicitly ruled out deploying any collector inside guest VMs
at all**, opt-in or otherwise. Tier 2 (`guest-vm-config.yaml`) is kept in
this repo for reference only and is not part of the proposed solution.
Practically, this means guest-OS-internal visibility (gap #4) and
static-memory VM pressure alerting (gap #2) depend entirely on Tier 1.5
(PowerShell Direct guest probe) for this engagement, since Tier 2 is not an
option. Tier 1.5 is implemented and mechanism-validated against a real
nested-Hyper-V guest, but ships disabled (`guest_probe.enabled: false`)
pending fleet-wide go/no-go validation — treat these as
**solved-but-not-yet-production-enabled**, not as "solved by Tier 2," until
that validation completes.

## 4. Static-memory VMs have no memory-pressure signal from Tier 1

The `Hyper-V Dynamic Memory VM` object's "Current Pressure" counter only
exists for VMs with Dynamic Memory enabled. Static-memory VMs never populate
`vm.memory.current_pressure` — the `vm_memory_pressure_high` detector
silently never fires for them. Tier 1.5 (`internal/guestprobe`) closes this
via the guest's own OS-reported memory usage (`vm.guest.memory.used_percent`),
implemented and mechanism-validated but disabled pending fleet-wide go/no-go.
See `docs/known-gaps-remediation.md`, gap #2.

## 5. VM name extraction from Perfmon instance strings is best-effort, not guaranteed

`transform/vm_name` in `hypervisor-host-config.yaml` handles the instance
string formats observed in Microsoft's documentation and one real customer
POC. Known incomplete cases:
- Pass-through disks and DVD/ISO-mounted drives are not attributable to a VM
  name by path alone (`Get-VMHardDiskDrive` doesn't return them either) —
  ~19–23% of VHD instances went unmatched in the reference POC. See gap #3.
- Duplicate VM names on the same host produce identical `vm.name` values
  after suffix-stripping (gap #6) — their metrics will merge onto one
  Splunk Observability Cloud entity. Validated against a real Hyper-V host:
  this holds for both the CPU counter (Windows appends a "#N" suffix that we
  strip) and the Network Adapter counter (Windows disambiguates via an
  embedded GUID instead, which our extraction discards) — same practical
  outcome via two different raw mechanisms. This is a customer naming-hygiene
  problem, not fixable purely in collector config; preserving the Network
  GUID as a workaround would break entity correlation for every VM, not just
  duplicate-named ones (see `docs/known-gaps-remediation.md`, "Additional
  findings" section).
- Any future Perfmon instance-string format not covered by the existing
  `Split`/`IsMatch` chain will fall through to the generic first-pass copy
  (raw instance string as `vm.name`), producing a messy but non-fatal entity
  name rather than a silent drop.

## 6. `vm.storage.latency` unit is unconfirmed

Documented by Microsoft as 100ns ticks, but not independently verified
against a raw counter value in a live environment. The
`vm_storage_latency_high` detector ships `disabled = true` for this reason —
do not enable it until validated. See `docs/known-gaps-remediation.md`,
gap #5.

## 7. Network series completeness is unverified (~20% of VMs missing in one POC)

A previously-unconfirmed root cause was found and fixed via real-hardware
validation: `hypervisor-host-config.yaml` requested a counter name
(`Bytes Total/sec`) that doesn't exist on the `Hyper-V Virtual Network
Adapter` or `Hyper-V Virtual Switch` Perfmon objects — only `Bytes/sec`,
`Bytes Sent/sec`, and `Bytes Received/sec` exist there. This caused a total
(not partial) outage of `vm.net.bytes_total` and `hyperv.vswitch.bytes_total`
on any fresh deployment. Fixed by correcting the counter name to
`Bytes/sec`; confirmed populating in Splunk Observability Cloud after the
fix. This does not necessarily fully explain the original POC's ~20%
partial-match figure on an already-deployed cluster — if a residual gap
remains after applying this fix, fall back to the original candidates (VMs
with no vNIC, powered-off VMs, uncovered instance-string edge case) and
spot-check before building any "missing series" detector. See gap #7.

## 8. No availability/power-state monitoring

Perfmon counters only report data for running VMs — a powered-off VM simply
stops emitting, which looks identical to "the collector missed a scrape."
Tier 1 (Perfmon) does not attempt VM/host up-down detection at all — that
requires a different source of truth. Tier 0 (`hyperv-scvmm-poller`, this
same repo) closes this by polling SCVMM directly for
`hyperv_vm_up`/`hyperv_host_up`. See gap #1.

## 9. This is a metrics/logs pipeline + dashboards/detectors — not full navigator parity

The vSphere integration in Splunk Observability Cloud includes curated
navigators, entity relationships, and content built by the Infrastructure
Monitoring content team over time. This repo provides the OTel Collector
configs and Terraform-defined dashboards/detectors to approximate that for
Hyper-V, but does not claim equivalent polish, coverage, or long-term
maintenance commitment. Treat it as a starting point for a field engagement,
not a packaged product.

## 10. Storage metrics can be misattributed to the wrong VM if disk naming doesn't follow convention

`transform/vm_name`'s storage extraction trusts that a VHDX's filename
matches its owning VM's name (Hyper-V's own default when a VM's disk is
created via `New-VM` without a custom path). Validated on a real Hyper-V
host: the raw Perfmon instance string is the disk's **full path**, so two
VMs with identically-named VHDX files in different directories don't
collide with each other — but if a customer's environment doesn't follow
the name-matches-convention (cloned templates, renamed/reused VHDX files),
this will confidently attribute storage metrics to the wrong VM, not just
fail to attribute them. There is no reliable signal in the raw counter to
detect or correct this. See `docs/known-gaps-remediation.md`, "Additional
findings" section.
