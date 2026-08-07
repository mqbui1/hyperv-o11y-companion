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
| Guest-side collector config | `otel-collector/guest-vm-config.yaml` | **Ruled out for this customer** — reference only, do not deploy |
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

### 3. Guest-side collector (`guest-vm-config.yaml`) — do not deploy for this customer

This customer has explicitly ruled out deploying any collector inside guest
VMs, opt-in or otherwise. `otel-collector/guest-vm-config.yaml` is kept in
this repo for reference only (e.g. other engagements without this
constraint). For this customer, gaps #2 and #4 remain **open**, pending the
Tier 1.5 (PowerShell Direct guest probe) go/no-go decision — see
`docs/known-gaps-remediation.md` and `docs/phase3-guest-probe-plan.md`.

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
