# Creates the Gen2 Rocky Linux 9 guest VM used to validate Tier 1.6
# (internal/guestfs) - downloads the Rocky 9 ISO, attaches it plus the
# OEMDRV kickstart VHD (built by 04-build-rocky-oemdrv-vhd.ps1), and boots.
#
# Rocky Linux 9 is used instead of RHEL because real RHEL ISOs require a
# gated Red Hat account login; Rocky is a freely-downloadable RHEL rebuild
# with identical GPT+XFS defaults.
#
# IMPORTANT - manual step required after this script runs (cannot be
# automated reliably on this host, see docs/full-test-environment-runbook.md
# "Known instability"):
#   1. Open Hyper-V Manager, VMConnect into vm-rocky1.
#   2. On the GRUB menu, the DEFAULT highlighted entry is
#      "Test this media & install Rocky Linux 9" - do NOT let it auto-boot.
#      It runs a full ISO checksum first, which is slow and looks like a
#      hang. Use the arrow keys to select the plain "Install Rocky Linux 9"
#      entry instead, then press Enter.
#   3. The kickstart (OEMDRV auto-detected) drives the rest unattended.
#      Wait for it to reach a real "login:" prompt (confirms install
#      actually completed - climbing VM uptime alone is NOT a reliable
#      signal; a VM stuck at the GRUB menu for hours still shows uptime).
#
# Do this over VMConnect, not WMI Msvm_Keyboard.TypeKey injection or
# run-command - both are unreliable for anything that triggers a real
# boot-state transition on this class of host and can hang the VM Agent.

param(
    [string]$IsoUrl = "https://download.rockylinux.org/pub/rocky/9/isos/x86_64/Rocky-9-latest-x86_64-minimal.iso",
    [string]$IsoPath = "C:\VMs\ISOs\Rocky-9-minimal.iso",
    [string]$VmName = "vm-rocky1",
    [string]$VhdPath = "C:\VMs\vm-rocky1.vhdx",
    [string]$OemdrvVhdPath = "C:\VMs\rocky-oemdrv.vhd"
)

New-Item -ItemType Directory -Force -Path (Split-Path $IsoPath) | Out-Null

if (-not (Test-Path $IsoPath)) {
    Write-Host "Downloading Rocky 9 ISO..."
    # Without this, Invoke-WebRequest's progress-bar rendering badly
    # throttles large downloads (a multi-GB ISO took 70+ minutes without
    # this; seconds/minutes with it).
    $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest -Uri $IsoUrl -OutFile $IsoPath
    $ProgressPreference = 'Continue'
}

New-VHD -Path $VhdPath -SizeBytes 20GB -Dynamic | Out-Null

New-VM -Name $VmName -MemoryStartupBytes 2GB -Generation 2 `
    -VHDPath $VhdPath -SwitchName "Default Switch"

Set-VMFirmware -VMName $VmName -EnableSecureBoot Off
Add-VMDvdDrive -VMName $VmName -Path $IsoPath
Add-VMHardDiskDrive -VMName $VmName -Path $OemdrvVhdPath

# Boot order: DVD first so it actually installs from the ISO.
$dvd = Get-VMDvdDrive -VMName $VmName
Set-VMFirmware -VMName $VmName -FirstBootDevice $dvd

Start-VM -Name $VmName

Write-Host ""
Write-Host "VM '$VmName' started. Now VMConnect into it and select the plain"
Write-Host "'Install Rocky Linux 9' GRUB entry (see script header comment)."
