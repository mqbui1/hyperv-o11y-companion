// skipreason.go classifies a FilesystemUsedPercent error into a
// low-cardinality tag suitable for a metric dimension. This exists so the
// v1 -> v2 coverage roadmap (which unsupported layout to add next: LVM?
// ext4/btrfs? something else?) is driven by what actually shows up across
// the pilot fleet, not by guesswork — see docs/architecture-gaps-overview.md.
package guestfs

import "errors"

// SkipReason is a fixed, small set of tags. Deliberately not the raw
// error string: that would make skip_reason an unbounded/high-cardinality
// metric dimension, which is exactly what this telemetry is trying to
// avoid becoming.
type SkipReason string

const (
	SkipReasonDifferencing  SkipReason = "differencing"   // legacy; no longer emitted after 2f
	SkipReasonLVM           SkipReason = "lvm"
	SkipReasonMBR           SkipReason = "mbr"
	SkipReasonEncrypted     SkipReason = "encrypted"
	SkipReasonOtherFS       SkipReason = "ext4_btrfs" // any non-XFS Linux filesystem
	SkipReasonNoLinuxPart   SkipReason = "no_linux_partition"
	SkipReasonChainCorrupt  SkipReason = "chain_corrupt"  // parent missing/unreadable/invalid (2f)
	SkipReasonChainModified SkipReason = "chain_modified" // parent modified after child was created (2f)
	SkipReasonOther         SkipReason = "other"
)

// ClassifySkipReason maps an error returned by FilesystemUsedPercent to a
// SkipReason tag. Returns "" for a nil error (nothing to classify).
// Anything not recognized — including transient I/O errors on the VHDX
// file itself, which are a different concern from "unsupported layout" —
// falls back to SkipReasonOther rather than growing the tag set per
// caller.
func ClassifySkipReason(err error) SkipReason {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrChainCorrupt):
		return SkipReasonChainCorrupt
	case errors.Is(err, ErrChainModified):
		return SkipReasonChainModified
	case errors.Is(err, ErrDifferencingDisk):
		// Legacy sentinel; no longer returned after 2f for valid chains.
		return SkipReasonDifferencing
	case errors.Is(err, ErrLVM):
		return SkipReasonLVM
	case errors.Is(err, ErrNotMBR):
		// Neither GPT nor MBR recognised — truly unrecognised partition scheme.
		return SkipReasonMBR
	case errors.Is(err, ErrLUKS):
		return SkipReasonEncrypted
	case errors.Is(err, ErrNotXFS):
		return SkipReasonOtherFS
	case errors.Is(err, ErrNoLinuxPartition):
		return SkipReasonNoLinuxPart
	default:
		return SkipReasonOther
	}
}
