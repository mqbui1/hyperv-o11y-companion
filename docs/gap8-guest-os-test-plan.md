# Gap #8 (`guest_os` accuracy) — Customer Test/Verify Plan

Gap #4 (`vm.guest.filesystem.used_percent` / `vm.guest.memory.used_percent`)
has been verified end-to-end against a real Splunk Observability Cloud org
using the existing nested-Hyper-V Azure test VM — see
`docs/phase3-guest-probe-plan.md` and `docs/known-gaps-remediation.md` gap #4.

Gap #8 is a different tier (Tier 0, `hyperv-scvmm-poller`) with a much
heavier prerequisite: it needs a real **System Center Virtual Machine
Manager (SCVMM) management server**, which does not exist in the current
test environment (a single nested Hyper-V host with no SCVMM console). That
is out of scope to stand up in this session — this is a runbook for the
**customer** to execute against their own SCVMM environment (or a
throwaway SCVMM test deployment), not something provisioned here.

## 1. Prerequisites

- An SCVMM management server reachable from wherever `hyperv-scvmm-poller`
  runs (the "console box" — same requirement as the customer's existing
  `collect-scvmm-metrics.ps1` / `enrich-vm-guest-os.ps1`).
- The `VirtualMachineManager` PowerShell module installed on that box
  (`internal/scvmm` shells out to it — see README.md "Why Go still shells
  out to PowerShell for SCVMM").
- A read-only SCVMM account (same one the existing scripts use is fine).
- A Splunk Observability Cloud access token with dimension-write scope
  (the metadata API call is a `PUT` to `/v2/dimension/vm/<name>`).
- At least 3 real VMs in SCVMM's inventory with **known, different**
  `OperatingSystem` values to make the spot-check meaningful:
  1. A Windows VM (SCVMM `OperatingSystem` contains "Windows").
  2. A Linux VM (SCVMM `OperatingSystem` contains one of: linux, ubuntu,
     red hat, rhel, centos, suse, debian, oracle, rocky, alma, fedora).
  3. A VM SCVMM reports as `Unknown` or blank `OperatingSystem` — this is
     the case gap #8 was originally about (99 unknown + 28 untagged in the
     real POC).

## 2. Set up credentials

On the box that will run `hyperv-scvmm-poller` (Windows Credential Manager,
generic credential type — same mechanism `host-companion`'s guest probe
uses):

```powershell
cmdkey /generic:hyperv-o11y/scvmm /user:<DOMAIN>\<svc-account> /pass:<password>
cmdkey /generic:hyperv-o11y/splunk-token /user:token /pass:<SPLUNK_ACCESS_TOKEN>
```

## 3. Configure `scvmm-poller.yaml`

Copy `config/scvmm-poller.yaml` and edit:

```yaml
scvmm:
  server: "<your-vmm-server-fqdn>"
  cluster: "<cluster-name-or-empty-for-whole-estate>"
  credential_name: "hyperv-o11y/scvmm"

splunk:
  realm: "<your-realm>"          # e.g. us1
  credential_name: "hyperv-o11y/splunk-token"

otlp:
  endpoint: "http://localhost:4318"   # or your collector's OTLP endpoint
  insecure: true

guest_os:
  interval: 5m       # shorten from the 1h default ONLY for this test, so
                      # you don't have to wait an hour to see results
  secure_boot_fallback: false   # leave off for the first pass — needs
                                 # cross-domain remote rights to hosts,
                                 # test separately once the basic path works
  name_heuristic: true
  linux_name_markers: [<your Linux/K8s/appliance naming markers>]

host_include: []      # empty = all hosts in cluster scope; narrow this to
                       # a test host/cluster if you don't want to touch
                       # every VM's dimension on first run
```

Set `guest_os.interval` back to something like `1h` (or leave the default)
once verification is done — 5m is only for making the test fast.

## 4. Install and start the service

Use the MSI (`installer/`) with the `scvmm-poller` feature selected, or run
the binary directly for a first foreground smoke test before installing as
a service (same technique used to validate the guest probe — see
`internal/winsvc/run_windows.go`'s dev-run mode):

```powershell
& "C:\Program Files\Splunk\HyperVO11yCompanion\bin\scvmm-poller.exe" -config C:\path\to\scvmm-poller.yaml
```

Watch stdout/stderr directly in this foreground mode — `pollGuestOS` logs a
summary line every cycle:

```
guest_os: set=<n> unchanged=<n> dup-skipped=<n> errors=<n>
```

- `errors > 0` means the SCVMM PowerShell call or the metadata API `PUT`
  failed — check the full error text logged just above the summary line
  (`scvmm vms poll (guest_os) failed: ...` or
  `guest_os update failed for <vm>: ...`).
- `dup-skipped > 0` is expected and correct if you have duplicate VM names
  in SCVMM's inventory (see gap #6) — those are intentionally skipped, not
  a bug.

Once the foreground run produces a clean `set=` line with no errors,
Ctrl+C it and install/start the real Windows Service
(`hyperv-scvmm-poller`) instead for ongoing operation.

## 5. Verify in Splunk Observability Cloud

Wait one full `guest_os.interval` past service start, then:

1. **Infrastructure Monitoring** → find one of your 3 test VMs by
   `host.name`/`vm.name` → open its entity → **Properties** (or **Custom
   Properties**) panel. Confirm three properties are present:
   - `guest_os` — should be `windows`, `linux`, `other`, or `unknown`.
   - `guest_os_detail` — the raw SCVMM `OperatingSystem` string it was
     classified from.
   - `guest_os_source` — `scvmm` (classified directly from SCVMM's field)
     or `heuristic` (only if `name_heuristic: true` and the VM name matched
     one of your `linux_name_markers`).
2. Spot-check all 3 VMs:
   - Windows VM → `guest_os: windows`, `guest_os_source: scvmm`.
   - Linux VM → `guest_os: linux`, `guest_os_source: scvmm`.
   - Previously-unknown VM → either still `unknown` (if
     `name_heuristic` didn't match its name) or `linux` with
     `guest_os_source: heuristic` (if it did) — confirm this matches your
     expectation for that specific VM's name, not just that *some* value
     got set.
3. **Metrics Finder** → any metric carrying the `vm` dimension for one of
   these VMs (e.g. `vm.cpu.total_run_time`) → confirm the same
   `guest_os`/`guest_os_detail`/`guest_os_source` properties appear when
   you inspect that dimension's properties from a chart — this is what
   "zero-MTS, retroactively applies to every metric already carrying that
   VM's dimension" (README.md) means in practice: it's a dimension
   property, not a separate metric series.

## 6. Edge cases worth testing deliberately

- **Duplicate VM names**: if two VMs share a name in SCVMM's inventory,
  confirm `dup-skipped` in the log increments and neither VM's `guest_os`
  property gets overwritten with the other's data (same protection
  documented for gap #6).
- **Re-running with `name_heuristic: false`**: temporarily flip this to
  `false` and confirm previously-`heuristic`-tagged VMs revert to
  `unknown` on the next cycle, and `guest_os_source` updates accordingly —
  confirms the source field is trustworthy, not stale.
- **Secure Boot fallback** (`secure_boot_fallback: true`): only test this
  once you're ready to grant the service cross-domain remote rights to the
  Hyper-V hosts themselves (not just SCVMM) — this is explicitly disabled
  by default in `config/scvmm-poller.yaml` because of that extra
  permission requirement. Not required for the basic gap #8 verification
  above.

## 7. Rollback

If anything looks wrong, stop `hyperv-scvmm-poller` — it only ever writes
the three `guest_os*` custom properties via GET→merge→PUT (`internal/metadata`),
so no other dimension data is touched. Existing values from the customer's
current `enrich-vm-guest-os.ps1` (if still running in parallel during a
shadow/parity test — see `docs/parity-testing-and-cutover.md`) will simply
be overwritten on each `hyperv-scvmm-poller` cycle with whatever it
computes; stopping the service leaves the properties at their last-written
value rather than reverting them.
