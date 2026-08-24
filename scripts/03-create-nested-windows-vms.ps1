# Creates the OS-less Windows nested test VMs used to validate Tier 1
# (perfcounters) - including a deliberately duplicate-named pair to confirm
# real Perfmon "#N" instance-suffixing behavior against ground truth
# (validates known-gaps-remediation.md gap #6, not just the synthetic test
# in otel-collector/test/).
#
# VMs don't need a bootable OS to populate CPU/memory/network/storage-
# existence counters - Hyper-V allocates resources as soon as a VM is
# powered on. A default virtual switch is created automatically when the
# Hyper-V role installs.

$vhdDir = "C:\VMs"
New-Item -ItemType Directory -Force -Path $vhdDir | Out-Null

New-VM -Name "vm-alpha" -MemoryStartupBytes 512MB -Generation 1 `
    -NewVHDPath "$vhdDir\Virtual Hard Disks-vm-alpha.vhdx" -NewVHDSizeBytes 5GB `
    -SwitchName "Default Switch"
Start-VM -Name "vm-alpha"

# Second VM, same name - exercises the real duplicate-name "#1" suffix.
New-VM -Name "vm-alpha" -MemoryStartupBytes 512MB -Generation 1 `
    -NewVHDPath "$vhdDir\Virtual Hard Disks-vm-alpha-2.vhdx" -NewVHDSizeBytes 5GB `
    -SwitchName "Default Switch"
Start-VM -Name "vm-alpha"

Write-Host "`nRaw instance strings (confirm '#1' suffix on the second vm-alpha):"
(Get-Counter -ListSet "Hyper-V Hypervisor Virtual Processor").PathsWithInstances
(Get-Counter -ListSet "Hyper-V Virtual Storage Device").PathsWithInstances
