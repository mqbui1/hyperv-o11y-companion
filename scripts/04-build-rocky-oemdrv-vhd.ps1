# Builds a small VHD, formats it FAT32 with the volume label OEMDRV, and
# copies ks-rocky9-minimal.cfg to its root. Anaconda auto-detects any
# attached block device labeled OEMDRV containing /ks.cfg and uses it as the
# kickstart source automatically - no GRUB boot-parameter editing needed.
#
# Run on the Hyper-V host (over RDP), as Administrator.

param(
    [string]$VhdPath = "C:\VMs\rocky-oemdrv.vhd",
    [string]$KickstartSource = "$PSScriptRoot\ks-rocky9-minimal.cfg"
)

New-Item -ItemType Directory -Force -Path (Split-Path $VhdPath) | Out-Null

if (Test-Path $VhdPath) { Remove-Item $VhdPath -Force }
New-VHD -Path $VhdPath -SizeBytes 10MB -Fixed | Out-Null
$disk = Mount-VHD -Path $VhdPath -Passthru | Get-Disk
Initialize-Disk -Number $disk.Number -PartitionStyle MBR
$partition = New-Partition -DiskNumber $disk.Number -UseMaximumSize -AssignDriveLetter
$volume = Format-Volume -Partition $partition -FileSystem FAT32 -NewFileSystemLabel "OEMDRV" -Confirm:$false

Copy-Item -Path $KickstartSource -Destination "$($partition.DriveLetter):\ks.cfg" -Force

Write-Host "OEMDRV VHD ready: $VhdPath (label OEMDRV, contains ks.cfg)"
Dismount-VHD -Path $VhdPath
