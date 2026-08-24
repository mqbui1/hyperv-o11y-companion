# Run as Administrator, on the Azure test host, over RDP or a single
# run-command invocation (this one is safe for run-command since it's a
# single non-interactive call, unlike anything that boots/reboots a nested
# guest - see docs/full-test-environment-runbook.md "Known instability" for
# why nested-guest-boot operations should NOT go through run-command).
#
# Installs the Hyper-V role and reboots. The VM will be briefly unreachable
# during the reboot - wait ~2-3 min before reconnecting.

Install-WindowsFeature -Name Hyper-V -IncludeManagementTools -Restart
