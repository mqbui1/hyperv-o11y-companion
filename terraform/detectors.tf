# =============================================================================
# Detectors mirroring the common Hyper-V failure modes referenced in the
# archived Splunk Add-on for Microsoft Hyper-V (VM health, hypervisor CPU,
# memory pressure, storage latency). Tune thresholds/durations per environment
# before rolling out to production.
# =============================================================================

resource "signalfx_detector" "vm_health_critical" {
  name = "Hyper-V: VM(s) in Critical Health"

  program_text = <<-EOF
    A = data('hyperv.health.critical', filter=filter('host.type', 'hypervisor')).sum(by=['host.name'])
    detect(when(A > 0)).publish('VM Health Critical')
  EOF

  rule {
    detect_label  = "VM Health Critical"
    severity      = "Critical"
    description   = "One or more VMs on this Hyper-V host reported Health Critical"
    notifications = []
  }
}

resource "signalfx_detector" "hypervisor_cpu_high" {
  name = "Hyper-V: Hypervisor Host CPU Sustained High"

  program_text = <<-EOF
    A = data('hyperv.host_cpu.total_run_time', filter=filter('host.type', 'hypervisor')).mean(by=['host.name'])
    detect(when(A > 90, lasting='10m')).publish('Hypervisor CPU High')
  EOF

  rule {
    detect_label  = "Hypervisor CPU High"
    severity      = "Warning"
    description   = "Hyper-V host CPU run time above 90% for 10+ minutes"
    notifications = []
  }
}

resource "signalfx_detector" "vm_memory_pressure_high" {
  name = "Hyper-V: VM Memory Pressure High"

  program_text = <<-EOF
    A = data('vm.memory.current_pressure', filter=filter('host.type', 'hypervisor_managed_vm')).mean(by=['host.name'])
    detect(when(A > 80, lasting='10m')).publish('VM Memory Pressure High')
  EOF

  rule {
    detect_label  = "VM Memory Pressure High"
    severity      = "Warning"
    description   = "Dynamic Memory pressure above 80% for 10+ minutes — VM may be memory-starved"
    notifications = []
  }
}

resource "signalfx_detector" "vm_storage_latency_high" {
  # Unit confirmed via customer real-fleet empirical analysis (a
  # multi-thousand-series sample) — the raw counter reports seconds, not the 100ns
  # ticks Microsoft's documentation claims.
  # otel-collector/hypervisor-host-config.yaml's transform/vm_storage_latency_scale
  # now converts vm.storage.latency to ms before export, so the threshold
  # below is in ms. See docs/known-gaps-remediation.md, gap #5.
  name = "Hyper-V: VM Virtual Disk Latency High"

  program_text = <<-EOF
    A = data('vm.storage.latency', filter=filter('host.type', 'hypervisor_managed_vm')).mean(by=['host.name'])
    detect(when(A > 20, lasting='5m')).publish('VM Storage Latency High')
  EOF

  rule {
    detect_label  = "VM Storage Latency High"
    severity      = "Warning"
    description   = "Virtual disk IO latency above 20ms for 5+ minutes"
    notifications = []
  }
}

resource "signalfx_detector" "vm_guest_filesystem_used_high" {
  # Tier 1.5 (PowerShell Direct guest probe, internal/guestprobe) — gap #4.
  # Ships disabled: host-companion's guest_probe.enabled defaults to false
  # pending the go/no-go criteria in docs/phase3-guest-probe-plan.md, so
  # this metric won't exist in most deployments yet. Enable both together.
  name = "Hyper-V: Guest Filesystem Used High (Tier 1.5)"

  program_text = <<-EOF
    A = data('vm.guest.filesystem.used_percent', filter=filter('host.type', 'hypervisor_managed_vm')).mean(by=['host.name', 'drive_letter'])
    detect(when(A > 85, lasting='15m')).publish('Guest Filesystem Used High')
  EOF

  rule {
    detect_label  = "Guest Filesystem Used High"
    severity      = "Warning"
    description   = "Guest filesystem used space above 85% for 15+ minutes (Tier 1.5, gap #4) — enable only for VMs in guest_probe.vm_include"
    disabled      = true
    notifications = []
  }
}

resource "signalfx_detector" "vm_guest_memory_used_high" {
  # Tier 1.5 (PowerShell Direct guest probe, internal/guestprobe) — gap #2.
  # Covers static-memory VMs, which vm_memory_pressure_high above cannot
  # (Hyper-V's Current Pressure counter only exists for Dynamic Memory
  # VMs). Ships disabled: host-companion's guest_probe.enabled defaults to
  # false pending the go/no-go criteria in docs/phase3-guest-probe-plan.md.
  name = "Hyper-V: Guest Memory Used High (Tier 1.5, static-memory VMs)"

  program_text = <<-EOF
    A = data('vm.guest.memory.used_percent', filter=filter('host.type', 'hypervisor_managed_vm')).mean(by=['host.name'])
    detect(when(A > 90, lasting='10m')).publish('Guest Memory Used High')
  EOF

  rule {
    detect_label  = "Guest Memory Used High"
    severity      = "Warning"
    description   = "Guest OS-reported memory used above 90% for 10+ minutes (Tier 1.5, gap #2) — enable only for VMs in guest_probe.vm_include"
    disabled      = true
    notifications = []
  }
}

resource "signalfx_detector" "vmms_migration_failures" {
  name = "Hyper-V: Live Migration Failures (VMMS Event 21026)"

  program_text = <<-EOF
    A = data('hyperv.vmms.migration_failures', filter=filter('host.type', 'hypervisor')).sum(by=['host.name']).sum(over='10m')
    detect(when(A > 0)).publish('VMMS Migration Failures')
  EOF

  rule {
    detect_label  = "VMMS Migration Failures"
    severity      = "Warning"
    description   = "One or more live-migration failures (Event ID 21026) on this host in the last 10m — root cause of elevated VMMS load in a real customer POC (docs/known-gaps-remediation.md, gap #9)"
    notifications = []
  }
}
