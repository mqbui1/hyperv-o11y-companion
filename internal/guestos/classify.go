// Package guestos ports the guest-OS classification logic from
// enrich-vm-guest-os.ps1's Get-GuestOs / Get-NameHeuristicOs functions, so
// scvmm-poller can set the guest_os dimension property without shelling out
// to a second script.
package guestos

import (
	"regexp"
	"strings"
)

var (
	reUnknown = regexp.MustCompile(`(?i)^unknown`)
	reWindows = regexp.MustCompile(`(?i)windows`)
	reLinux   = regexp.MustCompile(`(?i)linux|ubuntu|red\s*hat|rhel|cent\s*os|suse|debian|oracle|rocky|alma|fedora`)
)

// Classify normalizes SCVMM's free-text OperatingSystem string into
// windows/linux/other/unknown — same rules as Get-GuestOs in
// enrich-vm-guest-os.ps1.
func Classify(rawOS string) string {
	trimmed := strings.TrimSpace(rawOS)
	if trimmed == "" || reUnknown.MatchString(trimmed) {
		return "unknown"
	}
	if reWindows.MatchString(trimmed) {
		return "windows"
	}
	if reLinux.MatchString(trimmed) {
		return "linux"
	}
	return "other"
}

// NameHeuristic is the last-resort, opt-in fallback for still-"unknown" VMs:
// a substring match against systematic Linux/K8s/appliance naming. Returns
// "" (no match) rather than guessing when nothing matches — same
// conservative behavior as Get-NameHeuristicOs.
func NameHeuristic(vmName string, markers []string) string {
	lower := strings.ToLower(vmName)
	for _, m := range markers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return "linux"
		}
	}
	return ""
}
