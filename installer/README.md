# installer/

WiX v4 source producing a single MSI with two independently selectable
features:

| Feature | Installs | Deploy on |
|---|---|---|
| `ScvmmPollerFeature` | `hyperv-scvmm-poller` service + `scvmm-poller.yaml` | Central SCVMM console box only (`CULSPLUNKO11Y01`) |
| `HostCompanionFeature` | `hyperv-host-companion` service + `host-companion.yaml` | Every physical Hyper-V host |

Both are registered as native Windows Services (`ServiceInstall`/
`ServiceControl`, `Start="auto"`), so they start on boot and stop cleanly on
uninstall — no Task Scheduler entries.

## Build

Prerequisites: Go toolchain, WiX v4 CLI (`dotnet tool install --global wix`).

```powershell
cd installer
.\build.ps1
```

Produces `installer\out\HyperVO11yCompanion.msi`.

## Install

```powershell
# Console box: SCVMM poller only
msiexec /i HyperVO11yCompanion.msi ADDLOCAL=ScvmmPollerFeature /qn

# Every Hyper-V host: host companion only
msiexec /i HyperVO11yCompanion.msi ADDLOCAL=HostCompanionFeature /qn
```

Config files (`config\scvmm-poller.yaml`, `config\host-companion.yaml`) are
installed with `NeverOverwrite="yes"` — re-running the installer (e.g. for
an upgrade) will not clobber an operator's already-tuned config. Edit the
config and restart the relevant service (`Restart-Service
hyperv-scvmm-poller` / `hyperv-host-companion`) to pick up changes; neither
service currently watches its config file for live reload.

## Before shipping to a customer

- Regenerate every placeholder GUID in `main.wxs` (`Package/@UpgradeCode`
  and every `Component/@Guid`) — the values checked in are NOT safe to reuse
  across installs.
- `hyperv-scvmm-poller` still needs its SCVMM/Splunk credentials set via
  Windows Credential Manager (`internal/creds`) before its first start — the
  MSI does not prompt for or provision these; see the main README's
  "Credentials" section.
