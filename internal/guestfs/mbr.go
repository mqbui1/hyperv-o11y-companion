// mbr.go parses a legacy MBR (Master Boot Record) partition table from
// sector 0 of the logical disk.
//
// MBR scope: 4 primary partitions only. Extended/logical partitions
// (types 0x05, 0x0F) are not followed — deferred to a follow-up.
// NTFS/Windows partitions (type 0x07) are mapped to typeWindowsBasicData
// and probed via ReadNTFSBitmap (2h).
//
// Sector size: MBR always lives at byte offset 0 and uses 512-byte LBA
// addressing regardless of physical sector size (the guest's logical
// sector geometry is always 512 from the VHDX's perspective in v1).
package guestfs

import (
	"encoding/binary"
	"fmt"
)

const (
	mbrSignatureOffset = 510
	mbrPartTableOffset = 446
	mbrPartEntrySize   = 16
	mbrMaxPrimary      = 4
	mbrSectorBytes     = 512

	// MBR partition type bytes of interest.
	mbrTypeLinux    = 0x83 // Linux ext2/ext3/ext4/XFS
	mbrTypeLinuxLVM = 0x8E // Linux LVM physical volume
	mbrTypeNTFS     = 0x07 // Windows NTFS/HPFS (2h: probed via ReadNTFSBitmap)
	// Not handled:
	//   0x0B/0x0C = FAT32
	//   0x05/0x0F = Extended (logical partitions — deferred)
	//   0x27 = Windows Recovery
)

// ErrNotMBR is returned when the disk has no valid MBR signature
// (0x55AA at bytes 510-511). Used as a fallback signal in probe.go after
// ErrNotGPT is already confirmed: if neither GPT nor MBR is found, the
// disk layout is unrecognized.
var ErrNotMBR = fmt.Errorf("%w: no MBR boot signature (0x55AA) at sector 0", ErrUnsupported)

// ReadMBRPartitionTable reads sector 0 from r and delegates to
// parseMBRSector so the partition-table parsing logic is independently
// testable without a real VHDX.
func ReadMBRPartitionTable(r *Reader) ([]Partition, error) {
	sector := make([]byte, mbrSectorBytes)
	if _, err := r.ReadAt(sector, 0); err != nil {
		return nil, fmt.Errorf("reading MBR sector: %w", err)
	}
	return parseMBRSector(sector)
}

// parseMBRSector extracts partition entries from a raw 512-byte MBR
// sector. Only returns entries for Linux-filesystem and Linux-LVM typed
// partitions — other types are skipped (see package doc). Uses the same
// Partition type as gpt.go so FindRootFilesystem works unchanged.
func parseMBRSector(sector []byte) ([]Partition, error) {
	if len(sector) < mbrSectorBytes {
		return nil, fmt.Errorf("guestfs: MBR sector too short (%d bytes)", len(sector))
	}
	if sector[mbrSignatureOffset] != 0x55 || sector[mbrSignatureOffset+1] != 0xAA {
		return nil, ErrNotMBR
	}

	var out []Partition
	for i := 0; i < mbrMaxPrimary; i++ {
		off := mbrPartTableOffset + i*mbrPartEntrySize
		entry := sector[off : off+mbrPartEntrySize]

		ptype := entry[4]
		lbaStart := binary.LittleEndian.Uint32(entry[8:12])
		lbaSectors := binary.LittleEndian.Uint32(entry[12:16])

		if ptype == 0x00 || lbaSectors == 0 {
			continue // empty entry
		}

		startByte := int64(lbaStart) * mbrSectorBytes
		endByte := int64(lbaStart+lbaSectors) * mbrSectorBytes

		switch ptype {
		case mbrTypeLinux:
			out = append(out, Partition{
				TypeGUID:  typeLinuxFilesystem,
				StartByte: startByte,
				EndByte:   endByte,
			})
		case mbrTypeLinuxLVM:
			out = append(out, Partition{
				TypeGUID:  typeLinuxLVM,
				StartByte: startByte,
				EndByte:   endByte,
			})
		case mbrTypeNTFS:
			out = append(out, Partition{
				TypeGUID:  typeWindowsBasicData,
				StartByte: startByte,
				EndByte:   endByte,
			})
		// 0x27 (Recovery), 0x05/0x0F (extended): skip.
		}
	}
	return out, nil
}
