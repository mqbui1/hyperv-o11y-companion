// Package diskattr resolves a raw Hyper-V Virtual Storage Device Perfmon
// instance string back to a VM using a hyperv.DiskMap — port of the
// resolution logic in collect-hyperv-vm-disk.ps1 (path-suffix match, with a
// VM-Id fallback for .vmgs instances). See docs/known-gaps-remediation.md
// gap #3 for the ~19-23% residual (DVD/ISO/pass-through disks) this
// deliberately does not resolve.
package diskattr

import (
	"strings"

	"github.com/splunk/hyperv-o11y-companion/internal/hyperv"
)

// Resolve returns the VM owning instance (a raw Perfmon instance string —
// typically a full VHD(X) path, or a .vmgs path keyed by VM GUID), and
// whether a match was found.
func Resolve(m *hyperv.DiskMap, instance string) (hyperv.VMDiskEntry, bool) {
	norm := strings.ToLower(instance)

	if e, ok := m.ByPath[norm]; ok {
		return e, true
	}
	// Suffix match: instance strings sometimes carry a different drive/share
	// prefix than what Get-VMHardDiskDrive reported (UNC vs local path to the
	// same file). Same fallback the original script uses.
	for path, e := range m.ByPath {
		if strings.HasSuffix(norm, path) || strings.HasSuffix(path, norm) {
			return e, true
		}
	}
	// .vmgs instances are keyed by VM GUID embedded in the filename, not a
	// VHD path — extract it and fall back to the ID index.
	if strings.HasSuffix(norm, ".vmgs") {
		for id, e := range m.ByID {
			if strings.Contains(norm, id) {
				return e, true
			}
		}
	}
	return hyperv.VMDiskEntry{}, false
}
