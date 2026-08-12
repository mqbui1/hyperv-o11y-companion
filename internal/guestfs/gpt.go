// gpt.go parses a GPT partition table (UEFI spec) directly off a
// guestfs.Reader's logical disk address space, to find the guest's root
// filesystem partition without mounting anything.
//
// v1 scope: GPT only, not legacy MBR. Generation 2 Hyper-V VMs (the only
// kind that can boot Linux with Secure Boot / UEFI, and the modern
// default for new VMs) require GPT, so this covers the real fleet — MBR
// support is not planned unless a customer surfaces Generation 1 Linux
// VMs specifically.
package guestfs

import (
	"encoding/binary"
	"fmt"
)

const (
	gptSectorSize = 512 // v1 assumes 512-byte logical sectors; 4Kn disks are not handled
	gptHeaderLBA  = 1
	gptSignature  = "EFI PART"
	partEntrySize = 128
)

// Partition-type GUIDs this package cares about (UEFI spec Appendix A /
// Linux distro conventions). Raw on-disk (little-endian-encoded) form.
var (
	typeLinuxFilesystem = mustGUID("0fc63daf-8483-4772-8e79-3d69d8477de4")
	typeLinuxLVM        = mustGUID("e6d6d379-f507-44c2-a23c-238f2a3df928")
	typeLinuxSwap       = mustGUID("0657fd6d-a4ab-43c4-84e5-0933c84b4f4f")
	typeESP             = mustGUID("c12a7328-f81f-11d2-ba4b-00a0c93ec93b")
)

// Partition is one GPT entry's location on the logical disk, in bytes.
type Partition struct {
	TypeGUID  guid
	StartByte int64
	EndByte   int64 // exclusive
	Name      string
}

// ErrNoLinuxPartition is returned when a GPT disk has no partition typed
// as a plain Linux filesystem — e.g. an all-LVM layout (see ErrLVM) or a
// non-Linux guest.
var ErrNoLinuxPartition = fmt.Errorf("guestfs: no Linux filesystem-type GPT partition found")

// ErrLVM is returned when the only Linux-typed partition found is an LVM
// physical volume — parsing LVM metadata to find the underlying logical
// volume's extents is not implemented in v1.
var ErrLVM = fmt.Errorf("guestfs: %w: root partition is an LVM physical volume (LVM not supported in v1)", ErrUnsupported)

// ReadPartitionTable parses the GPT on r and returns every partition
// entry (including non-Linux ones — callers decide what to do with them).
func ReadPartitionTable(r *Reader) ([]Partition, error) {
	header := make([]byte, gptSectorSize)
	if _, err := r.ReadAt(header, gptHeaderLBA*gptSectorSize); err != nil {
		return nil, fmt.Errorf("reading GPT header: %w", err)
	}
	if string(header[0:8]) != gptSignature {
		return nil, fmt.Errorf("%w: no GPT signature at LBA 1 (not a GPT disk, or 4Kn sectors)", ErrUnsupported)
	}
	partEntryLBA := binary.LittleEndian.Uint64(header[72:80])
	numEntries := binary.LittleEndian.Uint32(header[80:84])
	entrySize := binary.LittleEndian.Uint32(header[84:88])
	if entrySize < partEntrySize {
		return nil, fmt.Errorf("guestfs: unexpected GPT partition entry size %d", entrySize)
	}

	entries := make([]byte, uint64(numEntries)*uint64(entrySize))
	if _, err := r.ReadAt(entries, int64(partEntryLBA)*gptSectorSize); err != nil {
		return nil, fmt.Errorf("reading GPT partition entries: %w", err)
	}

	var out []Partition
	for i := uint32(0); i < numEntries; i++ {
		e := entries[uint64(i)*uint64(entrySize) : uint64(i)*uint64(entrySize)+partEntrySize]
		var typeGUID guid
		copy(typeGUID[:], e[0:16])
		if typeGUID == (guid{}) {
			continue // unused entry
		}
		startLBA := binary.LittleEndian.Uint64(e[32:40])
		endLBA := binary.LittleEndian.Uint64(e[40:48]) // inclusive per spec
		name := decodeUTF16LE(e[56:128])
		out = append(out, Partition{
			TypeGUID:  typeGUID,
			StartByte: int64(startLBA) * gptSectorSize,
			EndByte:   (int64(endLBA) + 1) * gptSectorSize,
			Name:      name,
		})
	}
	return out, nil
}

// FindRootFilesystem returns the largest plain Linux-filesystem-typed
// partition (skipping ESP/swap), or ErrLVM if only an LVM PV is present,
// or ErrNoLinuxPartition if neither is found. "Largest" (rather than
// "first") matters because a standard non-LVM Anaconda layout types both
// /boot and / as plain Linux filesystem partitions, with /boot (a small
// fixed-size partition, ~1GB) listed before / (which gets the rest of
// the disk) — picking the first match would silently report /boot's
// usage instead of the actual root filesystem's. A guest with other
// separate Linux filesystem partitions beyond /boot and / (e.g. a
// dedicated /var) is still only probed on its single largest one; full
// mount-point inventory is a follow-up, not a v1 requirement (gap #4
// asks for root filesystem visibility, not a full mount-point
// inventory).
func FindRootFilesystem(parts []Partition) (Partition, error) {
	var sawLVM bool
	var best Partition
	var found bool
	for _, p := range parts {
		switch p.TypeGUID {
		case typeLinuxFilesystem:
			if !found || (p.EndByte-p.StartByte) > (best.EndByte-best.StartByte) {
				best = p
				found = true
			}
		case typeLinuxLVM:
			sawLVM = true
		case typeESP, typeLinuxSwap:
			continue
		}
	}
	if found {
		return best, nil
	}
	if sawLVM {
		return Partition{}, ErrLVM
	}
	return Partition{}, ErrNoLinuxPartition
}

func decodeUTF16LE(b []byte) string {
	runes := make([]rune, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u := binary.LittleEndian.Uint16(b[i : i+2])
		if u == 0 {
			break
		}
		runes = append(runes, rune(u))
	}
	return string(runes)
}
