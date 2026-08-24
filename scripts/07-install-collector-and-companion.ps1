# Run on the Hyper-V test host (over RDP), as Administrator, after
# 06-build-and-stage-binaries.sh has uploaded the built artifacts.
#
# Installs the Splunk OTel Collector for Windows (Tier 1) and the
# hyperv-o11y-companion services (Tier 1/1.5 disk companion + Tier 1.6
# guestfs probe), and wires the test-host-specific config for this
# environment (Rocky guest included in guest_fs_probe.vm_include).
#
# See installer/README.md steps 5-11 in this repo for the full,
# customer-facing version of this procedure - this script automates that
# same procedure for the disposable test host.

param(
    [Parameter(Mandatory = $true)][string]$SplunkAccessToken,
    [Parameter(Mandatory = $true)][string]$SplunkRealm,
    [Parameter(Mandatory = $true)][string]$MsiSasUrl,           # from generate-staging-sas.sh BLOB_NAME=HyperVO11yCompanion.msi
    [string]$RepoRoot = "C:\hyperv-o11y-companion"               # local clone, for hypervisor-host-config.yaml
)

$stage = "C:\staging"
New-Item -ItemType Directory -Force -Path $stage | Out-Null

# --- 1. Splunk OTel Collector (Tier 1) ---
Write-Host "== Installing Splunk OTel Collector for Windows =="
Write-Host "Get the current install script URL from the Splunk Distribution of"
Write-Host "the OpenTelemetry Collector docs for your realm, then run e.g.:"
Write-Host '  & "$stage\splunk-otel-collector.ps1" -access_token $SplunkAccessToken -realm $SplunkRealm'
Write-Host "(Intentionally not hardcoded here - see installer/README.md step 5"
Write-Host " and this repo's nested-hyperv-azure-test-plan.md for why.)"
Write-Host ""
Write-Host "Once installed, replace its config:"
Copy-Item "$RepoRoot\otel-collector\hypervisor-host-config.yaml" `
    "C:\Program Files\Splunk\OpenTelemetry Collector\agent_config.yaml" -Force
Write-Host "Config replaced with hypervisor-host-config.yaml - restart the service:"
Restart-Service splunk-otel-collector

# NOTE: reinstalling/repairing the collector via the install script's
# -access_token/-realm flags regenerates agent_config.yaml from MSI defaults
# each time (no merge) - re-copy hypervisor-host-config.yaml and restart the
# service again after any reinstall.

# --- 2. hyperv-o11y-companion MSI (host-companion + guestfs-probe) ---
Write-Host "== Downloading companion MSI =="
Invoke-WebRequest -Uri $MsiSasUrl -OutFile "$stage\HyperVO11yCompanion.msi"

Write-Host "== Installing HostCompanionFeature + GuestFSProbeFeature =="
Start-Process msiexec.exe -ArgumentList `
    "/i `"$stage\HyperVO11yCompanion.msi`" ADDLOCAL=HostCompanionFeature,GuestFSProbeFeature /qn" `
    -Wait

$configDir = "C:\Program Files\Splunk\HyperVO11yCompanion\config"

# --- 3. Point guestfs-probe at the Rocky guest created by 05-provision-rocky-guest.ps1 ---
$guestfsConfig = @"
otlp:
  endpoint: "http://localhost:4318"
  insecure: true

guest_fs_probe:
  enabled: true
  vm_include: ["vm-rocky1"]
  sample_interval: 5m
  sample_timeout: 30s
"@
Set-Content -Path "$configDir\guestfs-probe.yaml" -Value $guestfsConfig -Force
Restart-Service hyperv-guestfs-probe

Write-Host ""
Write-Host "== Done =="
Write-Host "Get-Service splunk-otel-collector, hyperv-host-companion, hyperv-guestfs-probe"
Write-Host "should all show Running. See docs/full-test-environment-runbook.md Step 5"
Write-Host "for how to confirm data is actually landing in Splunk (realm/token gotchas)."
