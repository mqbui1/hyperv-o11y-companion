# Full Test Environment Runbook (Azure Nested Hyper-V)

Engineering-facing runbook for standing up the complete test environment used
to validate this repo end-to-end — Tier 1 (Perfmon/event-log collection),
Tier 1.5 (PowerShell Direct guest probe), and Tier 1.6 (VHDX host-side
guestfs read) — against real infrastructure, not just synthetic fixtures.

This supersedes/extends `nested-hyperv-azure-test-plan.md` (which only covers
Tier 1 perfcounter validation) with the additional steps needed for Tier
1.5/1.6: provisioning the disposable Azure host, creating a real Rocky Linux
9 guest, and installing the collector + companion services.

**Read "Known instability / do not automate" below before running anything.**
Several steps in this environment are deliberately manual/interactive
because the fully-automated path (Azure `run-command`, WMI keyboard
injection) has repeatedly hung or corrupted state on this class of
resource-constrained nested-virt host during real testing.

## Prerequisites

- Azure CLI (`az`), logged in (`az login`), with a subscription that has
  quota for a `Dv3`/`Ev3`-family-or-newer VM size and nested virtualization
  enabled (standard on Azure, not all clouds support this).
- Go toolchain locally (for cross-compiling the companion binaries) and,
  optionally, WiX v4 (`dotnet tool install --global wix`) to build the MSI —
  see `installer/README.md` for build prerequisites.
- A Splunk Observability Cloud **ingest access token** (not an org/admin
  token — see "Realm/token gotchas" below) and the org's realm (e.g. `us1`).
- An RDP client. Interactive RDP is required for several steps — this is
  not a fully unattended pipeline.

## Step 1 — Provision the Azure host

```bash
cd scripts
RESOURCE_GROUP=hyperv-test-rg LOCATION=eastus VM_NAME=hyperv-test-host \
ADMIN_USERNAME=azureuser ADMIN_PASSWORD='<strong-password>' \
./provision-test-host.sh
```

Creates the resource group, the host VM (`Standard_D4als_v7`, TrustedLaunch,
Windows Server 2025 Datacenter G2 — the exact combination this repo has
actually been validated against; a plain `Standard_D4s_v3` + `Win2022Datacenter`
also works if TrustedLaunch isn't available on your subscription/image
combo, per the simpler example in `nested-hyperv-azure-test-plan.md`), an
NSG rule opening RDP (3389), and a storage account for staging file
transfers (binaries, ISOs, kickstart VHDs) since public blob access is
commonly disabled on these accounts (`PublicAccessNotPermitted`).

RDP in once it's up (`az vm show -d ... --query publicIps`).

## Step 2 — Enable Hyper-V and validate counters

Over RDP, as Administrator:

```powershell
.\01-enable-hyperv-role.ps1   # installs the role, reboots
# wait ~2-3 min, reconnect, then:
.\02-verify-hyperv-counters.ps1
```

This is the first real validation gate — confirms Microsoft hasn't renamed
any Perfmon counter object `otel-collector/hypervisor-host-config.yaml`
depends on, on this specific Windows Server build.

## Step 3 — Create the Windows nested test VMs (Tier 1 validation)

```powershell
.\03-create-nested-windows-vms.ps1
```

Creates `vm-alpha` twice (same name, deliberately) to validate the real
Perfmon `#1` duplicate-name suffixing behavior against ground truth, not
just the synthetic collector test. See `nested-hyperv-azure-test-plan.md`
sections 3-4 for the full Tier 1 validation checklist (raw instance-string
formats, disk-latency unit confirmation, event log channels) once these VMs
are up.

## Step 4 — Provision the Rocky Linux 9 guest (Tier 1.6 validation)

```powershell
.\04-build-rocky-oemdrv-vhd.ps1
.\05-provision-rocky-guest.ps1
```

`05-provision-rocky-guest.ps1` downloads the Rocky 9 minimal ISO, creates a
Generation 2 VM (`vm-rocky1`), attaches the ISO plus the OEMDRV kickstart
VHD, and boots it. **Anaconda auto-detects any attached device labeled
OEMDRV containing `/ks.cfg`** and uses it as the kickstart source
automatically — no GRUB boot-parameter editing needed.

`ks-rocky9-minimal.cfg` deliberately uses `autopart --nolvm --fstype=xfs`
(not `--lvm`) — this reproduces the exact non-LVM, separate-`/boot` layout
that exposed the real `FindRootFilesystem` root-partition-selection bug
during the original validation pass (see `docs/known-gaps-remediation.md`
gap #4's "Additional findings" section). Do not add LVM here; a different,
separate guest is the right place to test LVM-layout behavior (currently
unsupported, `ErrLVM`).

**Manual step required — do this via VMConnect, not automation:**

1. Hyper-V Manager → right-click `vm-rocky1` → **Connect**.
2. On the GRUB menu, the **default highlighted entry is "Test this media &
   install Rocky Linux 9"** — do not let it auto-boot. It runs a full ISO
   checksum first, which is slow and easy to mistake for a hang.
3. Use arrow keys to select the plain **"Install Rocky Linux 9"** entry,
   press Enter.
4. The kickstart drives the rest unattended. Wait for a real `login:`
   prompt on the console (climbing `Get-VM` uptime alone is **not**
   reliable — a VM stuck at the GRUB menu for hours still shows increasing
   uptime).

## Step 5 — Build, stage, and install the companion services + collector

On your build machine (macOS/Linux):

```bash
STORAGE_ACCOUNT=<from step 1 output> ./06-build-and-stage-binaries.sh
STORAGE_ACCOUNT=<...> BLOB_NAME=HyperVO11yCompanion.msi ./generate-staging-sas.sh download
```

Copy the printed SAS URL, then back on the host (RDP):

```powershell
.\07-install-collector-and-companion.ps1 `
    -SplunkAccessToken '<ingest-access-token>' `
    -SplunkRealm 'us1' `
    -MsiSasUrl '<SAS URL from above>'
```

This installs the Splunk OTel Collector, swaps in
`otel-collector/hypervisor-host-config.yaml`, installs the companion MSI
(`HostCompanionFeature` + `GuestFSProbeFeature`), and points
`guestfs-probe.yaml` at `vm-rocky1`.

### Realm/token gotchas (hit for real during validation — read before debugging a 401)

- An org token with `authScopes: ["API"]` is **management-API-only**, not
  ingest-capable. It will pass collector-install validation but the
  `signalfx` exporter then gets a hard `401 Unauthorized` at runtime,
  regardless of realm. You need a genuine **ingest access token**, a
  different credential from the org token used for Terraform provisioning.
- **A realm/token mismatch produces the exact same 401 signature as a
  genuinely bad token** — the error does not distinguish "wrong realm" from
  "bad token." Confirm which org/realm a token actually belongs to before
  assuming the token itself is invalid.
- Reinstalling/repairing the collector via its install script's
  `-access_token`/`-realm` flags **regenerates `agent_config.yaml` from MSI
  defaults every time** (no merge with your prior edits) — re-copy
  `hypervisor-host-config.yaml` and restart the service after every
  reinstall, not just the first install.

## Step 6 — Verify

- `Get-Service splunk-otel-collector, hyperv-host-companion, hyperv-guestfs-probe`
  — all `Running`.
- In Splunk Infrastructure Monitoring, confirm `host.type=hypervisor` shows
  up within a few minutes, and `vm.guest.filesystem.used_percent` appears
  for `vm-rocky1` within one `sample_interval` (5m default).
- Cross-check the reported percentage against the VHDX's own allocated-size
  metadata (`Get-VHD -Path <vm-rocky1's vhdx path>` → `FileSize`) as a
  sanity check, the same way the original `/boot`-vs-root bug was caught —
  see `docs/known-gaps-remediation.md` gap #4.

## Known instability / do not automate

Learned the hard way on this specific class of resource-constrained
nested-virt host — worth reading before "fixing" any of this into a fully
unattended pipeline:

- **`az vm run-command invoke` is fine for short, non-interactive,
  non-boot-changing scripts** (e.g. installing a Windows feature, querying
  counters) but should **not** be used for anything that boots or reboots a
  nested guest. Only one script executes at a time per VM (`(Conflict) Run
  command extension execution is in progress` on any second call while one
  is in flight), there's a hard ~90-minute ceiling before
  `VMExtensionProvisioningTimeout`, and a hung script can leave the VM Agent
  itself unresponsive (`VMAgentStatusCommunicationError`), blocking **all**
  further run-command usage until the host is restarted. This stuck state
  has been observed to persist through a plain `az vm restart` — only a full
  `az vm deallocate` + start cycle reliably cleared it, and that can take
  40-50+ minutes.
- **WMI keyboard injection (`Msvm_Keyboard.TypeKey`) is unreliable for
  anything that triggers a real boot-state transition** (e.g. pressing
  Enter on a GRUB menu to actually boot an entry) — this hung for 30-40+
  minutes and left the VM Agent unresponsive on two separate occasions.
  Trivial, non-state-changing keypresses (e.g. arrow-key navigation) are
  fine. **Use VMConnect (interactive Hyper-V Manager console) for any
  boot-triggering keystroke instead** — this is what Step 4 above does.
- If `run-command` does get stuck: RDP directly into the host and do the
  rest of the work interactively (VMConnect for guest console access,
  compiled binaries transferred via a blob SAS URL run directly). This is
  how the actual Tier 1.6 end-to-end validation was completed after
  run-command became unreliable mid-session.
- **RDP copy/paste of a long single-line command can silently mangle it** —
  a visual line-wrap in the source (browser, chat UI) can get captured as a
  real newline on paste into the PowerShell console, truncating the
  command. Paste into Notepad first, `Ctrl+A`/`Ctrl+C` from there (never
  inserts real newlines for visual wrapping), then paste that into
  PowerShell.
- `Invoke-WebRequest` without `$ProgressPreference = 'SilentlyContinue'` is
  badly throttled for large downloads (a multi-GB ISO took 70+ minutes
  without the fix; seconds with it) — already applied in
  `05-provision-rocky-guest.ps1`.
- This host has only ~16GB RAM in the `Standard_D4als_v7` size used above —
  running many nested VMs simultaneously causes real memory pressure and
  instability. Stop/deallocate nested VMs you're not actively using.

## Teardown

```bash
az group delete --name hyperv-test-rg --yes --no-wait
```

Deletes the host VM, storage account, and all nested guest state along with
it — nothing here needs to survive a test pass. Nested Hyper-V test hosts
are billed hourly; don't leave one running idle.
