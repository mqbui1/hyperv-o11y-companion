package guestfs

import (
	"encoding/binary"
	"errors"
	"testing"
)

// makeMBRSector builds a minimal 512-byte MBR sector with up to 4
// primary partition entries. Each entry is (ptype, lbaStart, lbaSectors).
func makeMBRSector(entries ...struct{ ptype uint8; start, count uint32 }) []byte {
	sector := make([]byte, 512)
	sector[510] = 0x55
	sector[511] = 0xAA
	for i, e := range entries {
		if i >= 4 {
			break
		}
		off := 446 + i*16
		sector[off+4] = e.ptype
		binary.LittleEndian.PutUint32(sector[off+8:], e.start)
		binary.LittleEndian.PutUint32(sector[off+12:], e.count)
	}
	return sector
}

func TestParseMBRSector_NoSignature(t *testing.T) {
	sector := make([]byte, 512) // all zeros, no 0x55AA
	_, err := parseMBRSector(sector)
	if !errors.Is(err, ErrNotMBR) {
		t.Fatalf("want ErrNotMBR, got %v", err)
	}
}

func TestParseMBRSector_EmptyTable(t *testing.T) {
	sector := makeMBRSector() // valid signature, no entries
	parts, err := parseMBRSector(sector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 0 {
		t.Fatalf("expected 0 partitions, got %d", len(parts))
	}
}

func TestParseMBRSector_LinuxPartition(t *testing.T) {
	sector := makeMBRSector(struct{ ptype uint8; start, count uint32 }{0x83, 2048, 20971520})
	parts, err := parseMBRSector(sector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 partition, got %d", len(parts))
	}
	p := parts[0]
	if p.TypeGUID != typeLinuxFilesystem {
		t.Fatalf("expected typeLinuxFilesystem, got %v", p.TypeGUID)
	}
	if p.StartByte != 2048*512 {
		t.Fatalf("StartByte = %d, want %d", p.StartByte, 2048*512)
	}
	if p.EndByte != (2048+20971520)*512 {
		t.Fatalf("EndByte = %d, want %d", p.EndByte, (2048+20971520)*512)
	}
}

func TestParseMBRSector_LVMPartition(t *testing.T) {
	sector := makeMBRSector(struct{ ptype uint8; start, count uint32 }{0x8E, 2048, 10485760})
	parts, err := parseMBRSector(sector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 || parts[0].TypeGUID != typeLinuxLVM {
		t.Fatalf("expected 1 LVM partition, got %v", parts)
	}
}

func TestParseMBRSector_NTFSMapped(t *testing.T) {
	// 0x07 = NTFS — now mapped to typeWindowsBasicData for NTFS $Bitmap probe (2h).
	sector := makeMBRSector(struct{ ptype uint8; start, count uint32 }{0x07, 2048, 20971520})
	parts, err := parseMBRSector(sector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 || parts[0].TypeGUID != typeWindowsBasicData {
		t.Fatalf("NTFS (0x07) should map to typeWindowsBasicData, got %v", parts)
	}
}

func TestParseMBRSector_MixedTypes(t *testing.T) {
	// FAT32 (skipped) + Linux root (kept) + NTFS data (kept as Windows) + empty.
	sector := makeMBRSector(
		struct{ ptype uint8; start, count uint32 }{0x0C, 2048, 1024},       // FAT32 — skip
		struct{ ptype uint8; start, count uint32 }{0x83, 3072, 20971520},    // Linux — keep
		struct{ ptype uint8; start, count uint32 }{0x07, 20974592, 2097152}, // NTFS — keep
		struct{ ptype uint8; start, count uint32 }{0x00, 0, 0},              // empty
	)
	parts, err := parseMBRSector(sector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 partitions (Linux + Windows), got %d: %v", len(parts), parts)
	}
	if parts[0].TypeGUID != typeLinuxFilesystem {
		t.Fatalf("first partition should be Linux, got %v", parts[0].TypeGUID)
	}
	if parts[1].TypeGUID != typeWindowsBasicData {
		t.Fatalf("second partition should be Windows Basic Data, got %v", parts[1].TypeGUID)
	}
}

func TestClassifySkipReason_ErrNotMBR(t *testing.T) {
	if got := ClassifySkipReason(ErrNotMBR); got != SkipReasonMBR {
		t.Fatalf("ClassifySkipReason(ErrNotMBR) = %q, want %q", got, SkipReasonMBR)
	}
}
