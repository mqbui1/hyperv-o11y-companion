# Capabilities and Metrics Reference

Generic reference for any Hyper-V environment — not specific to any one
customer's constraints. What this solution emits, tier by tier, and what's
already visualized in Splunk Observability Cloud out of the box. Which
tiers you actually deploy depends on your own guest-VM policy (see
"Choosing your tiers" at the end).

## Metrics catalog

### Tier 0 — SCVMM console (`hyperv-scvmm-poller`)

| Metric | Type | Unit | What it tells you |
|---|---|---|---|
| `hyperv_host_up` | Gauge (0/1) | — | Whether a physical Hyper-V host is powered on, sourced from SCVMM's host status (not Perfmon, which has no concept of "off") |
| `hyperv_vm_up` | Gauge (0/1) | — | Whether a VM is powered on, sourced from SCVMM's VM PowerState |
| `hyperv_host_memory_used_mb` | Gauge | MB | Host memory capacity used, from SCVMM |
| `guest_os` | Dimension property (not a metric) | — | Guest OS classification (SCVMM's OperatingSystem field → Secure Boot template fallback → optional naming heuristic), retroactively applied to every metric already carrying that VM's dimension |

### Tier 1 — every Hyper-V host, standard host telemetry

| Metric | Unit | What it tells you |
|---|---|---|
| `host.cpu.processor_time` | % | Physical host CPU busy time |
| `host.memory.available` | MBy | Physical memory available for allocation |
| `host.memory.committed_pct` | % | Ratio of committed bytes to commit limit |
| `host.disk.free_space` | % | Free space on the host's logical disks |
| `host.net.bytes_total` | By/s | Host network interface throughput |

### Tier 1 — every Hyper-V host, hypervisor-wide health (one value per host)

| Metric | Unit | What it tells you |
|---|---|---|
| `hyperv.health.ok` | {vms} | Count of VMs in healthy state |
| `hyperv.health.critical` | {vms} | Count of VMs in critical state |
| `hyperv.logical_processors` | {cpus} | Logical processors in the hypervisor |
| `hyperv.virtual_processors` | {cpus} | Virtual processors across all partitions |
| `hyperv.host_cpu.total_run_time` | % | Host logical processor total run time |
| `hyperv.memory.current_pressure` | % | Current system-wide VM memory pressure |
| `hyperv.vswitch.bytes_total` | By/s | Virtual switch total throughput (all switches) |

### Tier 1 — every Hyper-V host, per-VM resource allocation (host-visible)

One receiver instance covers every VM on the host — no per-VM config.

| Metric | Unit | What it tells you |
|---|---|---|
| `vm.cpu.total_run_time` | % | vCPU total run time |
| `vm.cpu.guest_run_time` | % | vCPU guest-code run time |
| `vm.memory.physical` | MBy | Physical memory assigned to the VM |
| `vm.memory.current_pressure` | % | Current memory pressure (Dynamic Memory VMs only — see gap #2 in `known-gaps-remediation.md`) |
| `vm.net.bytes_total` | By/s | VM vNIC total throughput |
| `vm.storage.read_bytes` | By/s | VM virtual disk read throughput |
| `vm.storage.write_bytes` | By/s | VM virtual disk write throughput |
| `vm.storage.latency` | ms | VM virtual disk IO latency (empirically confirmed and scale-corrected from raw seconds) |

### Tier 1 companion — every Hyper-V host, `hyperv-host-companion` (via OTLP)

Same underlying Hyper-V Virtual Storage Device counters as `vm.storage.*`
above, but resolved to a VM name via an in-memory VHD-path map
(`Get-VM | Get-VMHardDiskDrive`) instead of raw Perfmon instance-string
parsing — corrects for the ~19–23% of instances Perfmon's instance string
alone can't attribute (DVD/ISO/pass-through disks).

| Metric | Unit | What it tells you |
|---|---|---|
| `vm.disk.latency` | ms | VM virtual disk IO latency, disk-map-attributed |
| `vm.disk.read_bytes_sec` | By/s | VM virtual disk read throughput, disk-map-attributed |
| `vm.disk.write_bytes_sec` | By/s | VM virtual disk write throughput, disk-map-attributed |

### Tier 1.5 — PowerShell Direct guest probe (optional, `internal/guestprobe`)

Reads inside the guest OS over VMBus, with no guest network path, no guest
firewall rule, and no agent process running inside the guest. Ships
disabled by default (`guest_probe.enabled: false`) pending fleet-wide
go/no-go validation — see `docs/phase3-guest-probe-plan.md`.

| Metric | Unit | What it tells you |
|---|---|---|
| `vm.guest.filesystem.used_percent` | % | Guest fixed-volume used space (per drive letter) — actual guest filesystem usage, not host-visible virtual disk file size |
| `vm.guest.memory.used_percent` | % | Guest's own OS-reported memory usage — works for **static-memory VMs**, unlike `vm.memory.current_pressure` above which only exists for Dynamic Memory VMs |

### Event-derived metric (any tier with `windowseventlogreceiver`)

| Metric | Unit | What it tells you |
|---|---|---|
| `hyperv.vmms.migration_failures` | count | Live-migration failures (Event ID 21026 on the VMMS-Admin channel), converted from raw event log into an alertable metric via the `count` connector — the general pattern for making any event-log channel detector-eligible |

### Tier 2 — in-guest OTel Collector (optional, `guest-vm-config.yaml`)

Every VM running this becomes a separately-billed host. Full guest-OS
visibility via the standard `hostmetrics` receiver plus one legacy-parity
counter set:

| Metric | Unit | What it tells you |
|---|---|---|
| `hostmetrics` receiver (`cpu`, `memory`, `disk`, `filesystem`, `network`, `paging`, `processes` scrapers) | (OTel semantic conventions) | Full guest-OS-internal visibility: real CPU/memory/disk/filesystem/network utilization, process-level data — everything Tier 1/1.5 structurally cannot see |
| `guest.disk.free_space` | % | Guest logical disk free space — optional legacy metric-name parity with the archived Splunk Add-on for Microsoft Hyper-V |
| `guest.disk.free_mb` | MBy | Guest logical disk free space in MB — same parity purpose |

## What's already visualized (Terraform-provisioned)

### Dashboard: "Hyper-V: Hypervisor Overview" (one row per host)

| Chart | Metric |
|---|---|
| Hypervisor Host CPU | `hyperv.host_cpu.total_run_time` |
| VMs in Critical Health | `hyperv.health.critical` |
| Hypervisor Memory Pressure | `hyperv.memory.current_pressure` |
| Virtual Switch Throughput | `hyperv.vswitch.bytes_total` |
| Live Migration Failures | `hyperv.vmms.migration_failures` |

### Dashboard: "Hyper-V: VM Detail" (per VM)

| Chart | Metric | Tier |
|---|---|---|
| VM CPU | `vm.cpu.total_run_time` | 1 |
| VM Memory Pressure | `vm.memory.current_pressure` | 1 |
| VM Virtual Disk Latency | `vm.storage.latency` | 1 |
| VM Network Throughput | `vm.net.bytes_total` | 1 |
| Guest Filesystem Used | `vm.guest.filesystem.used_percent` | 1.5 (chart exists; populates only if `guest_probe.enabled: true`) |
| Guest Memory Used | `vm.guest.memory.used_percent` | 1.5 (same) |

### Detectors (`terraform/detectors.tf`)

| Detector | Metric | Threshold | Ships |
|---|---|---|---|
| VM(s) in Critical Health | `hyperv.health.critical` | > 0 | Enabled |
| Hypervisor Host CPU Sustained High | `hyperv.host_cpu.total_run_time` | > 90% for 10m | Enabled |
| VM Memory Pressure High | `vm.memory.current_pressure` | > 80% for 10m | Enabled |
| VM Virtual Disk Latency High | `vm.storage.latency` | > 20ms for 5m | Enabled |
| Guest Filesystem Used High | `vm.guest.filesystem.used_percent` | > 85% for 15m | **Disabled** — enable alongside `guest_probe.enabled` |
| Guest Memory Used High | `vm.guest.memory.used_percent` | > 90% for 10m | **Disabled** — enable alongside `guest_probe.enabled` |
| Live Migration Failures | `hyperv.vmms.migration_failures` | > 0 in 10m | Enabled |

`vm.disk.*` (Tier 1 companion) is exported but not yet wired into a chart
or detector — add one if disk-map-attributed latency/throughput (rather
than raw Perfmon-attributed `vm.storage.*`) is the preferred source of
truth for your environment.

## Choosing your tiers

- **Tiers 0, 1, and 1 companion** are the baseline for any Hyper-V
  environment — no guest-VM policy decision needed, deploy on the SCVMM
  console box and every physical host.
- **Tier 1.5** is the right choice if you want guest filesystem/memory
  visibility but don't want to deploy a collector inside guest VMs at all —
  PowerShell Direct over VMBus, no guest network path, no agent process.
  Requires guest Integration Services and a credential valid inside the
  guest.
- **Tier 2** is the right choice if guest-level visibility beyond
  filesystem/memory (process state, application metrics) is required and
  the extra per-VM billed-host cost is acceptable.
- **Future idea, not gap-driven, not built:** Windows Event Forwarding — a
  native Windows mechanism that could centralize event-log visibility (host +
  guest) without per-VM collector deployment. Not part of this proposal; no
  customer-POC gap requires it.

See `docs/architecture.md` for the full tier diagram and resource-attribute
strategy, and `docs/deployment-guide.md` for install/configure steps
(including a pilot-enablement procedure for Tier 1.5).
