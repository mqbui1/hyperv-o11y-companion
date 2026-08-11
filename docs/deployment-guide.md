# Deployment Guide

Field delivery, install, and test/verify runbook for the Hyper-V Observability
Accelerator. This is a field-delivered accelerator (Terraform configs + OTel
Collector YAML), not a packaged/Splunkbase product — the customer runs these
steps themselves, with FDE/solutions support as needed.

## What ships

| Component | File(s) | Scope |
|---|---|---|
| Dashboards + detectors | `terraform/*.tf` | One-time, org-wide |
| Host-side collector config | `otel-collector/hypervisor-host-config.yaml` | Every Hyper-V host |
| Gap tracking / validation checklist | `docs/known-gaps-remediation.md` | Reference during test/verify |
| Architecture reference | `docs/architecture.md` | Resource-attribute strategy, vm.name extraction |
| Limitations reference | `docs/limitations.md` | What this accelerator cannot do |

## Delivery

Deliver as a zip or git clone of this repo handed to:
- **Splunk org admin** on the customer side — runs the Terraform once
- **Hyper-V/Windows admin** on the customer side — installs the collector on
  hosts (and optionally guests)

No Splunkbase packaging — this is Terraform + YAML the customer runs
directly, so they retain ownership and can modify thresholds/config for their
environment without waiting on a vendor release cycle.

## Install / configure

### 1. Provision dashboards + detectors (one-time, ~5 minutes)

```
cd terraform
cp terraform.tfvars.example terraform.tfvars   # fill in splunk_access_token + splunk_realm
terraform init
terraform apply
```

Requires an **org token** (admin scope) to create dashboards/detectors —
different from the ingest token used in step 2/3.

### 2. Install the Splunk OTel Collector for Windows on every Hyper-V host

- Install the Splunk Distribution of the OpenTelemetry Collector for Windows
  (MSI installer) on each Hyper-V host, pushed via whatever the customer
  already uses for host-level software (GPO/SCCM) — this is standard
  Windows-fleet tooling, nothing Hyper-V-specific.
- Replace the installed default config with
  `otel-collector/hypervisor-host-config.yaml`.
- Set environment variables on each host:
  - `SPLUNK_ACCESS_TOKEN` — ingest token (not the org token from step 1)
  - `SPLUNK_REALM`
- Restart the `splunk-otel-collector` service.

This step touches every host and is what fills the "Hyper-V: Hypervisor
Overview" dashboard.

### 3. Tier 1.5 pilot enablement (optional — requires customer go/no-go first)

This customer has explicitly ruled out deploying any collector inside guest
VMs, opt-in or otherwise. Gaps #2 and #4 are instead solved via Tier 1.5
(PowerShell Direct guest probe, mechanism-validated), but remain disabled
in production pending fleet-wide go/no-go validation — see
`docs/known-gaps-remediation.md` and `docs/phase3-guest-probe-plan.md`.

Tier 1.5 (`internal/guestprobe`, wired into `host-companion`) closes gaps #2
and #4 **without deploying anything inside the guest** — no in-guest agent,
no guest network path. It ships disabled
(`guest_probe.enabled: false`) and stays that way until the three go/no-go
criteria in `docs/phase3-guest-probe-plan.md` are validated against the
customer's real fleet:

1. `Invoke-Command -VMId` latency/load at real fleet density (VMs-per-host)
2. Guest Integration Services coverage across the fleet's actual guest OS
   versions
3. Whether a single shared guest-local credential is acceptable, or
   per-VM/per-domain credential handling is required

**Do not flip `guest_probe.enabled` to `true` fleet-wide.** The steps below
are for a small, supervised pilot on a handful of VMs only, to gather the
data needed to answer criteria 1–3 above — mirrors the "start with a small
pilot cluster" recommendation under Rollout sequencing below.

1. **Provision the guest-local credential** — a read-only account valid
   *inside* the pilot guests (via the customer's existing guest-VM
   provisioning process; this is not a domain-admin or host-level account).
   On each pilot host, store it in Windows Credential Manager under the name
   `host-companion.yaml` expects (`guest_probe.credential_name`, default
   `hyperv-o11y/guest-probe`):
   ```powershell
   cmdkey /generic:hyperv-o11y/guest-probe /user:<guest-local-user> /pass:<secret>
   ```
   Confirm guest Integration Services are current on each pilot VM
   (`Get-VM <name> | Select IntegrationServicesState` — must report `Up to
   date`) before including it below; this is exactly go/no-go criterion #2,
   spot-checked per pilot VM.

2. **Scope the pilot to specific VMs** — edit
   `host-companion.yaml` on each pilot host:
   ```yaml
   guest_probe:
     enabled: true
     vm_include: ["PilotVM01", "PilotVM02"]   # explicit names or glob patterns; empty = nothing probed
     sample_interval: 5m
     sample_timeout: 30s
     credential_name: "hyperv-o11y/guest-probe"
   ```
   `vm_include` is opt-in and empty by default — no VM is probed just because
   `enabled: true` is set. Keep this list to the pilot VMs only.

3. **Restart the service** (`Restart-Service hyperv-host-companion`) and
   confirm in the service's log output that the guest-probe ticker starts
   firing (`guest probe: vm=<name>: ...` on failure, or successful gauge
   exports with no matching log line on success).

4. **Confirm the metrics land** — `vm.guest.filesystem.used_percent`
   (tagged `vm.name`, `drive_letter`) and `vm.guest.memory.used_percent`
   (tagged `vm.name`) should appear on the pilot VMs' entities in Splunk
   Observability Cloud within one `sample_interval`. The `guest_filesystem_used`
   and `guest_memory_used` charts on the "VM Detail" dashboard
   (`terraform/dashboards.tf`) will populate for pilot VMs only.

5. **Gather go/no-go data during the pilot** — session latency/CPU
   impact on the host (criterion #1), any IC-related probe failures logged
   for VMs beyond what step 1 already screened (criterion #2), and whether
   the shared-credential model held up to the customer's security review
   (criterion #3). Do not widen `vm_include` or roll out to additional hosts
   until all three are resolved — `vm_guest_filesystem_used_high` and
   `vm_guest_memory_used_high` detectors also ship `disabled = true` in
   `terraform/detectors.tf` pending the same decision.

## Test / verify plan

1. **Ingestion sanity check** — in Infrastructure Monitoring, confirm hosts
   tagged `host.type=hypervisor` (physical hosts) and
   `host.type=hypervisor_managed_vm` (VMs) both appear within a few minutes
   of collector restart.
2. **Open the two dashboards** ("Hyper-V: Hypervisor Overview", "Hyper-V: VM
   Detail") and confirm every chart populates — no blank panels.
3. **Walk the known-gaps checklist** (`docs/known-gaps-remediation.md`) as
   the verification script — it's written against this exact customer's
   data:
   - Spot-check `vm.storage.latency`'s unit before ever enabling that
     detector (shipped `disabled = true` by default — gap #5)
   - Confirm the Perfmon `#N` duplicate-suffix strip fixed the malformed
     `vm.name` values (gap #6)
   - Confirm `vm.net.bytes_total` and `hyperv.vswitch.bytes_total` populate
     at all (both use `Bytes/sec`, not `Bytes Total/sec` — a total-outage bug
     fixed in `hypervisor-host-config.yaml`, gap #7). If a residual ~20% gap
     remains after that fix, cross-check against `Get-VMNetworkAdapter` output
   - Trigger or wait for a real Event ID 21026 and confirm it flows through
     to the `hyperv.vmms.migration_failures` metric and fires the
     `vmms_migration_failures` detector (gap #9)
4. **Sign-off gate** — only flip `vm_storage_latency_high`'s `disabled` to
   `false` in `terraform/detectors.tf` and re-apply Terraform once the unit
   has been independently validated. Do not enable on a customer's behalf
   without this validation step.

## Rollout sequencing recommendation

- Start with a small pilot cluster (mirrors the original POC's
  `HV-CLUSTER-01` scope) before fleet-wide host rollout.
- Environment cutover (`poc` -> production) requires no config change — see
  `docs/known-gaps-remediation.md`, gap #10 — just point
  `SPLUNK_REALM`/`SPLUNK_ACCESS_TOKEN` at the production org token and repeat
  step 2 across the remaining hosts.
