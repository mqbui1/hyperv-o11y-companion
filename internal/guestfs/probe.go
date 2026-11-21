// probe.go is the public entrypoint for Tier 1.6: given a VHDX path, open
// it, locate its root filesystem partition (GPT first, MBR fallback), and
// parse its superblock — entirely read-only, entirely from the host, with
// zero guest interaction of any kind.
package guestfs

import (
	"errors"
	"fmt"
)

// FilesystemUsedPercent opens the VHDX at path, locates its root
// filesystem partition, and returns that filesystem's used-space
// percentage. Returns ErrUnsupported (or a wrapped variant) for any
// unsupported layout — callers should log and skip, not treat as fatal.
//
// Partition-table resolution order:
//  1. GPT — the standard for Generation 2 Hyper-V VMs.
//  2. MBR — fallback for Generation 1 / legacy-BIOS VMs (2d).
//  3. Neither → ErrNotMBR → skip_reason=mbr.
//
// Filesystem resolution order (once a root partition is found):
//  1. XFS superblock — RHEL/Rocky/AlmaLinux default.
//  2. NTFS $Bitmap — Windows Server default (2h).
//  3. Neither → ErrNotXFS → skip_reason=ext4_btrfs (covers all other
//     Linux filesystems too; a dedicated ext4 parser is a future item).\
func FilesystemUsedPercent(path string) (float64, error) {
	r, err := Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening vhdx: %w", err)
	}
	defer r.Close()

	parts, err := ReadPartitionTable(r)
	if errors.Is(err, ErrNotGPT) {
		parts, err = ReadMBRPartitionTable(r)
		if err != nil {
			return 0, fmt.Errorf("reading partition table: %w", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("reading partition table: %w", err)
	}

	root, err := FindRootFilesystem(parts)
	if err != nil {
		return 0, err
	}

	// Try XFS first (Linux default), then NTFS (Windows default).
	xfsUsage, xfsErr := ReadXFSSuperblock(r, root)
	if xfsErr == nil {
		pct, ok := xfsUsage.UsedPercent()
		if !ok {
			return 0, fmt.Errorf("guestfs: zero-size XFS filesystem")
		}
		return pct, nil
	}
	if !errors.Is(xfsErr, ErrNotXFS) {
		// LUKS or I/O error — return as-is (ErrLUKS → skip_reason=encrypted).
		return 0, xfsErr
	}

	// XFS magic mismatch — try NTFS $Bitmap (2h).
	ntfsUsage, ntfsErr := ReadNTFSBitmap(r, root)
	if ntfsErr == nil {
		pct, ok := ntfsUsage.UsedPercent()
		if !ok {
			return 0, fmt.Errorf("guestfs: zero-size NTFS filesystem")
		}
		return pct, nil
	}
	// Not XFS and not NTFS — return the original XFS error so
	// ClassifySkipReason maps it to skip_reason=ext4_btrfs.
	return 0, xfsErr
}
