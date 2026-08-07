# Parity Testing and Cutover Plan

Scope: replacing the customer's existing PowerShell script pairs with the two
`hyperv-o11y-companion` Windows Services, without a gap in metric/dimension
coverage during the transition. Two independent cutovers — do not couple
them, since `scvmm-poller` and `host-companion` run on different boxes and
close different gaps.

## 1. `hyperv-scvmm-poller` replacing `collect-scvmm-metrics.ps1` + `enrich-vm-guest-os.ps1`

### Phase A — Shadow mode (no cutover yet)

1. Install `hyperv-scvmm-poller` on `CULSPLUNKO11Y01` alongside the existing
   Task Scheduler jobs — both run concurrently. Point the new service's
   `splunk.credential_name` at a **separate** SignalFx access token (or,
   at minimum, tag its output with a distinct `deployment.environment` /
   temporary resource attribute) so shadow-mode output never overwrites or
   double-counts the production metrics the scripts are already emitting.
2. Let both run for at least 24h (covers the poller's `metrics.interval` and
   `guest_os.interval`, plus at least one full SCVMM inventory refresh cycle).

### Phase B — Diff

Compare, per poll cycle:

| Check | How |
|---|---|
| `hyperv_vm_up` / `hyperv_host_up` coverage | Same set of `vm.name`/`host.name` values reporting from both sources? Any VM/host present in one but not the other? |
| Guest OS classification | For every VM, does the new service's `guest_os` dimension match what `enrich-vm-guest-os.ps1` last wrote? Flag mismatches — especially the Secure Boot fallback and naming-heuristic paths (gap #8), since those are the least deterministic parts of the classification logic. |
| Poll latency / SCVMM load | Does running both concurrently visibly increase SCVMM console load or `Get-SCVMHost`/`Get-SCVirtualMachine` latency? If so, stagger poll intervals during shadow mode only. |
| Duplicate VM name handling | Confirm the new service skips (not clobbers) ambiguous duplicate names the same way `enrich-vm-guest-os.ps1` does (gap #6 interaction). |

A `diff` script isn't included here since the comparison is against live
dimension/metric state in Splunk Observability Cloud, not files — use
`search_metric_time_series`/dimension lookups for the two sources' outputs
during the shadow window.

### Phase C — Cutover

1. Once Phase B shows zero unexplained coverage or classification gaps for a
   full 24–48h window, disable (don't yet delete) the two Task Scheduler
   jobs (`run-collect-scvmm-metrics.ps1`, `run-enrich-vm-guest-os.ps1`).
2. Point `hyperv-scvmm-poller`'s config at the real production SignalFx
   token/realm.
3. Monitor for one full week. Keep the disabled scheduled tasks and their
   scripts on disk (not deleted) as a rollback path — re-enabling a
   Scheduled Task is a one-click revert; reinstalling a Windows Service is
   not, if something regresses.
4. After one clean week, remove the Task Scheduler entries and archive
   (don't delete) the four PowerShell scripts and their credential files.

## 2. `hyperv-host-companion` replacing `build-hyperv-vm-disk-map.ps1` + `collect-hyperv-vm-disk.ps1`

Same shadow → diff → cutover shape, scoped per-host since this runs on every
Hyper-V host rather than centrally:

1. **Shadow**: install on 2–3 representative hosts first (mix of low/high VM
   density, and at least one host known to have pass-through/ISO-mounted
   disks per gap #3's residual ~19–23%). Existing scripts + their Task
   Scheduler entries keep running unmodified.
2. **Diff**: compare `vm.disk.{latency,read_bytes_sec,write_bytes_sec}`
   matched/unmatched counts the new service logs
   (`disk metrics: matched=%d unmatched=%d`) against the match rate the old
   script pair's own logging reports. These should land within a few points
   of each other (77–81% baseline) — a large regression means the in-memory
   disk map (`internal/hyperv.BuildDiskMap`) or `internal/diskattr.Resolve`
   has a bug relative to the original two-script JSON-cache-file handoff.
3. **Cutover**: per-host, once matched/unmatched parity holds for 24–48h,
   disable that host's disk-map Task Scheduler entries and let
   `host-companion` be the sole source. Roll host-by-host, not fleet-wide in
   one step — a bad disk-map build on one host should never be discovered
   simultaneously across the whole fleet.
4. Fleet-wide rollout only after the representative hosts from step 1 have
   been clean for a full week.

## Rollback

Both services are independent Windows Services with no shared state with the
scripts they replace — rollback is always "re-enable the Task Scheduler job,
stop/uninstall the new service," never a data-migration operation. This is
part of why Phase A/B intentionally keep the old scripts running until the
new service has proven itself, rather than a hard flag-day switch.
