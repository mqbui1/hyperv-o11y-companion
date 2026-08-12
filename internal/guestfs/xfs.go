// xfs.go parses an XFS superblock directly off a partition's first
// sector. XFS is RHEL 7/8/9's default root filesystem, so it's v1's only
// supported filesystem — ext4 (and any other Linux filesystem) returns a
// clear "not yet supported" error rather than misreading its bytes as XFS.
//
// Unlike VHDX (little-endian) and GPT (little-endian per the UEFI spec),
// XFS's on-disk format is big-endian — this file uses binary.BigEndian
// throughout, and vhdx.go/gpt.go use binary.LittleEndian; that split is
// intentional, not inconsistent.
//
// Field layout source: xfs_sb_t in xfsprogs/libxfs/xfs_format.h (the
// canonical reference for the on-disk superblock struct).
package guestfs

import (
	"encoding/binary"
	"fmt"
)

const (
	xfsMagic       = 0x58465342 // "XFSB"
	xfsSBLen       = 160        // enough to cover sb_fdblocks; real sb is larger but v1 only needs this
	xfsSBBlockSize = 4          // offset of sb_blocksize
	xfsDBlocksOff  = 8          // offset of sb_dblocks (uint64)
	xfsFdBlocksOff = 144        // offset of sb_fdblocks (uint64) — free data blocks
)

// XFSUsage is a coarse used-space summary read straight from the
// superblock — not a live df-style computation (it doesn't account for
// AG-level reservation nuances the same way `statfs` inside the guest
// would), but close enough for a "% used" alerting signal, and it's the
// same approximation xfs_info/xfs_db report before any in-guest
// filesystem-level rounding.
type XFSUsage struct {
	BlockSize   uint32
	TotalBlocks uint64
	FreeBlocks  uint64
}

// UsedPercent returns used-space percentage, or false if TotalBlocks is 0.
func (u XFSUsage) UsedPercent() (float64, bool) {
	if u.TotalBlocks == 0 {
		return 0, false
	}
	used := u.TotalBlocks - u.FreeBlocks
	return float64(used) / float64(u.TotalBlocks) * 100, true
}

// ErrNotXFS is returned when the partition's first sector doesn't start
// with the XFS magic number — including the common case of it actually
// being ext4, which v1 doesn't parse.
var ErrNotXFS = fmt.Errorf("guestfs: not an XFS superblock (magic mismatch — ext4/other filesystems not supported in v1)")

// ReadXFSSuperblock reads and parses the XFS superblock located at the
// start of part on r.
func ReadXFSSuperblock(r *Reader, part Partition) (XFSUsage, error) {
	buf := make([]byte, xfsSBLen)
	if _, err := r.ReadAt(buf, part.StartByte); err != nil {
		return XFSUsage{}, fmt.Errorf("reading XFS superblock: %w", err)
	}
	magic := binary.BigEndian.Uint32(buf[0:4])
	if magic != xfsMagic {
		return XFSUsage{}, ErrNotXFS
	}
	return XFSUsage{
		BlockSize:   binary.BigEndian.Uint32(buf[xfsSBBlockSize : xfsSBBlockSize+4]),
		TotalBlocks: binary.BigEndian.Uint64(buf[xfsDBlocksOff : xfsDBlocksOff+8]),
		FreeBlocks:  binary.BigEndian.Uint64(buf[xfsFdBlocksOff : xfsFdBlocksOff+8]),
	}, nil
}
