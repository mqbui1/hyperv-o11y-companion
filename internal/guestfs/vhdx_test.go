package guestfs

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// buildTestVHDX constructs a minimal, spec-conformant (for the fields
// this package reads) synthetic VHDX file on disk:
//   - blockSize = 1MiB, virtual disk size = 4MiB (4 logical blocks)
//   - block 0: protective MBR (unused) + a one-partition GPT whose single
//     entry is typed "Linux filesystem", spanning exactly block 1
//   - block 1: an XFS superblock at its start (dblocks=1000, fdblocks=250
//     -> 75% used)
//   - blocks 2/3: BAT state NOT_PRESENT (no backing file data needed)
//
// This exists because there's no real Windows/Hyper-V host in this
// environment to generate a real VHDX against — it validates the parser
// against the spec byte-for-byte, but doesn't replace real-hardware
// validation once this is deployed (same caveat Tier 1.5 flagged before
// its nested-Hyper-V test).
func buildTestVHDX(t *testing.T, hasParent bool) string {
	t.Helper()
	const (
		blockSize  = 1 * 1024 * 1024
		diskSize   = 4 * blockSize
		identOff   = 0
		regionOff  = 192 * 1024
		metaOff    = 1 * 1024 * 1024
		metaLen    = 4096
		batOff     = 2 * 1024 * 1024
		batLen     = 4 * 8 // 4 blocks * 8 bytes/entry
		payloadOff = 3 * 1024 * 1024
	)

	buf := make([]byte, payloadOff+2*blockSize) // room for block0+block1 payload

	// Identifier region.
	copy(buf[identOff:], identifierSignature)

	// Region table: 2 entries (BAT, Metadata).
	regionBuf := buf[regionOff : regionOff+64*1024]
	binary.LittleEndian.PutUint32(regionBuf[0:4], regionSignature)
	binary.LittleEndian.PutUint32(regionBuf[8:12], 2) // entry count
	writeRegionEntry(regionBuf[16:16+32], guidRegionBAT, batOff, batLen)
	writeRegionEntry(regionBuf[48:48+32], guidRegionMetadata, metaOff, metaLen)
	fixChecksum(regionBuf, 4)

	// Metadata region: header + 2 item entries (File Parameters, Virtual
	// Disk Size) + their payloads, placed after the item table.
	metaBuf := buf[metaOff : metaOff+metaLen]
	binary.LittleEndian.PutUint64(metaBuf[0:8], metadataSignature)
	binary.LittleEndian.PutUint16(metaBuf[10:12], 2) // entry count
	// Item payload offsets must land after the entry table (which spans
	// metaBuf[32:96] for 2 entries) — 64/72 would collide with it.
	const fileParamsItemOff = 96
	const vdiskSizeItemOff = 104
	writeMetaEntry(metaBuf[32:32+32], guidFileParameters, fileParamsItemOff, 8)
	writeMetaEntry(metaBuf[64:64+32], guidVirtualDiskSize, vdiskSizeItemOff, 8)
	binary.LittleEndian.PutUint32(metaBuf[fileParamsItemOff:fileParamsItemOff+4], blockSize)
	var flags uint32
	if hasParent {
		flags |= 0x2
	}
	binary.LittleEndian.PutUint32(metaBuf[fileParamsItemOff+4:fileParamsItemOff+8], flags)
	binary.LittleEndian.PutUint64(metaBuf[vdiskSizeItemOff:vdiskSizeItemOff+8], diskSize)

	// BAT: block0 and block1 fully present at payloadOff/payloadOff+blockSize;
	// block2/block3 not present (zero-filled reads, no backing data needed).
	batBuf := buf[batOff : batOff+batLen]
	writeBATEntry(batBuf[0:8], batStateFullyPresent, uint64(payloadOff)/(1024*1024))
	writeBATEntry(batBuf[8:16], batStateFullyPresent, uint64(payloadOff+blockSize)/(1024*1024))
	writeBATEntry(batBuf[16:24], batStateNotPresent, 0)
	writeBATEntry(batBuf[24:32], batStateNotPresent, 0)

	// Block 0 payload: GPT header at LBA1 (byte 512) + one partition
	// entry at LBA2 (byte 1024), spanning exactly block 1 (LBA 2048..4095).
	block0 := buf[payloadOff : payloadOff+blockSize]
	gptHeader := block0[512:1024]
	copy(gptHeader[0:8], gptSignature)
	binary.LittleEndian.PutUint64(gptHeader[72:80], 2) // PartitionEntryLBA
	binary.LittleEndian.PutUint32(gptHeader[80:84], 1) // NumberOfPartitionEntries
	binary.LittleEndian.PutUint32(gptHeader[84:88], partEntrySize)

	entry := block0[1024:1152]
	copy(entry[0:16], typeLinuxFilesystem[:])
	binary.LittleEndian.PutUint64(entry[32:40], 2048) // StartingLBA == start of block 1
	binary.LittleEndian.PutUint64(entry[40:48], 4095) // EndingLBA (inclusive) == end of block 1

	// Block 1 payload: XFS superblock at its very start.
	block1 := buf[payloadOff+blockSize : payloadOff+2*blockSize]
	binary.BigEndian.PutUint32(block1[0:4], xfsMagic)
	binary.BigEndian.PutUint32(block1[4:8], 4096)    // sb_blocksize
	binary.BigEndian.PutUint64(block1[8:16], 1000)   // sb_dblocks
	binary.BigEndian.PutUint64(block1[144:152], 250) // sb_fdblocks

	path := filepath.Join(t.TempDir(), "test.vhdx")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("writing test vhdx: %v", err)
	}
	return path
}

func writeRegionEntry(e []byte, g guid, fileOffset, length uint64) {
	copy(e[0:16], g[:])
	binary.LittleEndian.PutUint64(e[16:24], fileOffset)
	binary.LittleEndian.PutUint32(e[24:28], uint32(length))
}

func writeMetaEntry(e []byte, g guid, offset, length uint32) {
	copy(e[0:16], g[:])
	binary.LittleEndian.PutUint32(e[16:20], offset)
	binary.LittleEndian.PutUint32(e[20:24], length)
}

func writeBATEntry(e []byte, state uint64, fileOffsetMB uint64) {
	binary.LittleEndian.PutUint64(e, (fileOffsetMB<<20)|state)
}

// fixChecksum recomputes and writes the CRC32C checksum for a 64KB
// region-table-shaped structure, matching verifyChecksum's own logic.
func fixChecksum(buf []byte, checksumOff int) {
	binary.LittleEndian.PutUint32(buf[checksumOff:checksumOff+4], 0)
	sum := crc32.Checksum(buf, crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(buf[checksumOff:checksumOff+4], sum)
}

func TestEndToEnd_XFSRootPartition(t *testing.T) {
	path := buildTestVHDX(t, false)
	pct, err := FilesystemUsedPercent(path)
	if err != nil {
		t.Fatalf("FilesystemUsedPercent: %v", err)
	}
	if pct != 75.0 {
		t.Errorf("used percent = %v, want 75.0", pct)
	}
}

func TestOpen_DifferencingDiskUnsupported(t *testing.T) {
	path := buildTestVHDX(t, true)
	_, err := Open(path)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open(differencing disk) err = %v, want ErrUnsupported", err)
	}
}

func TestFindRootFilesystem_LVMOnly(t *testing.T) {
	parts := []Partition{{TypeGUID: typeLinuxLVM}}
	_, err := FindRootFilesystem(parts)
	if !errors.Is(err, ErrLVM) {
		t.Fatalf("FindRootFilesystem(LVM-only) err = %v, want ErrLVM", err)
	}
}

func TestFindRootFilesystem_PicksLargestOverFirst(t *testing.T) {
	// Mirrors a real non-LVM Anaconda layout: ESP, then a small /boot
	// (also typed "Linux filesystem", listed first), then the much
	// larger real root partition. The first match must NOT win.
	boot := Partition{TypeGUID: typeLinuxFilesystem, StartByte: 0, EndByte: 1024 * 1024 * 1024}                       // 1GiB
	root := Partition{TypeGUID: typeLinuxFilesystem, StartByte: 1024 * 1024 * 1024, EndByte: 20 * 1024 * 1024 * 1024} // ~19GiB
	parts := []Partition{{TypeGUID: typeESP}, boot, root}
	got, err := FindRootFilesystem(parts)
	if err != nil {
		t.Fatalf("FindRootFilesystem: %v", err)
	}
	if got != root {
		t.Errorf("FindRootFilesystem picked %+v, want the larger root partition %+v", got, root)
	}
}

func TestFindRootFilesystem_NoneFound(t *testing.T) {
	parts := []Partition{{TypeGUID: typeESP}, {TypeGUID: typeLinuxSwap}}
	_, err := FindRootFilesystem(parts)
	if !errors.Is(err, ErrNoLinuxPartition) {
		t.Fatalf("FindRootFilesystem(no linux fs) err = %v, want ErrNoLinuxPartition", err)
	}
}

func TestReadXFSSuperblock_WrongMagic(t *testing.T) {
	path := buildTestVHDX(t, false)
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	// Block 0 (GPT/MBR data) is not an XFS superblock.
	_, err = ReadXFSSuperblock(r, Partition{StartByte: 0})
	if !errors.Is(err, ErrNotXFS) {
		t.Fatalf("ReadXFSSuperblock(non-xfs) err = %v, want ErrNotXFS", err)
	}
}
