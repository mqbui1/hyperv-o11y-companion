# Run after 01-enable-hyperv-role.ps1's reboot completes.
#
# First real validation point: confirms Microsoft hasn't renamed any of the
# counter objects otel-collector/hypervisor-host-config.yaml depends on, on
# this specific Windows Server build.

Get-Counter -ListSet "Hyper-V*" | Select-Object CounterSetName

$required = @(
    "Hyper-V Hypervisor",
    "Hyper-V Dynamic Memory VM",
    "Hyper-V Virtual Network Adapter",
    "Hyper-V Virtual Storage Device",
    "Hyper-V Virtual Machine Health Summary"
)
$present = (Get-Counter -ListSet "Hyper-V*").CounterSetName
foreach ($name in $required) {
    if ($present -contains $name) {
        Write-Host "OK    $name"
    } else {
        Write-Host "MISSING  $name" -ForegroundColor Red
    }
}
