package guestfs

import (
	"encoding/binary"
	"errors"
	"testing"
)

// makeNTFSBootSector builds a minimal 512-byte NTFS boot sector.
func makeNTFSBootSector(bps uint16, spc uint8, totalSectors uint64, mftLCN uint64, cpmft uint8) []byte {
	s := make([]byte, 512)
	copy(s[3:], "NTFS    ")
	binary.LittleEndian.PutUint16(s[11:], bps)
	s[13] = spc
	binary.LittleEndian.PutUint64(s[40:], totalSectors)
	binary.LittleEndian.PutUint64(s[48:], mftLCN)
	s[64] = cpmft
	return s
}

func TestParseNTFSBootSector_Valid(t *testing.T) {
	// 512 BPS, 8 SPC (4 KiB clusters), 20 GiB volume, MFT at LCN 786432, 0xF6 (-10) → 1024-byte records
	s := makeNTFSBootSector(512, 8, 41943040, 786432, 0xF6)
	info, err := parseNTFSBootSector(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.BytesPerSector != 512 {
		t.Fatalf("BPS = %d", info.BytesPerSector)
	}
	if info.SectorsPerCluster != 8 {
		t.Fatalf("SPC = %d", info.SectorsPerCluster)
	}
	if info.BytesPerMFTRecord != 1024 { // 2^10
		t.Fatalf("BytesPerMFTRecord = %d, want 1024", info.BytesPerMFTRecord)
	}
}

func TestParseNTFSBootSector_OEMIDMismatch(t *testing.T) {
	s := make([]byte, 512)
	copy(s[3:], "FAT32   ")
	_, err := parseNTFSBootSector(s)
	if !errors.Is(err, ErrNotNTFS) {
		t.Fatalf("want ErrNotNTFS, got %v", err)
	}
}

func TestParseNTFSBootSector_PositiveClustersPerRecord(t *testing.T) {
	// cpmft = 1 → 1 cluster × 512 BPS × 8 SPC = 4096 bytes
	s := makeNTFSBootSector(512, 8, 41943040, 786432, 1)
	info, err := parseNTFSBootSector(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.BytesPerMFTRecord != 4096 {
		t.Fatalf("BytesPerMFTRecord = %d, want 4096", info.BytesPerMFTRecord)
	}
}

func TestDecodeDataRuns_SingleRun(t *testing.T) {
	// Header 0x21: offLen=2, lenLen=1
	// runLen byte = 0x40 (64 clusters)
	// offset bytes (LE uint16) = 0x10, 0x00 → delta=16 → LCN 16
	buf := []byte{0x21, 0x40, 0x10, 0x00, 0x00}
	runs, err := decodeDataRuns(buf, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].StartLCN != 16 {
		t.Fatalf("StartLCN = %d, want 16", runs[0].StartLCN)
	}
	if runs[0].Length != 0x40 {
		t.Fatalf("Length = %d, want 64", runs[0].Length)
	}
}

func TestDecodeDataRuns_SparseRun(t *testing.T) {
	// Header 0x01: offLen=0 (sparse), lenLen=1
	buf := []byte{0x01, 0x20, 0x00}
	runs, err := decodeDataRuns(buf, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 1 || runs[0].StartLCN != -1 || runs[0].Length != 0x20 {
		t.Fatalf("unexpected sparse run: %v", runs)
	}
}

func TestDecodeDataRuns_NegativeDelta(t *testing.T) {
	// Two runs: first at LCN 100 (length 10), second at delta -50 → LCN 50 (length 5)
	buf := []byte{
		0x11, 10, 100, // run 1: len=10, delta=+100 → LCN 100
		0x11, 5, 0xCE, // run 2: len=5, delta=0xCE (= -50 as int8) → LCN 100-50=50
		0x00,
	}
	runs, err := decodeDataRuns(buf, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].StartLCN != 100 {
		t.Fatalf("run[0].StartLCN = %d, want 100", runs[0].StartLCN)
	}
	if runs[1].StartLCN != 50 {
		t.Fatalf("run[1].StartLCN = %d, want 50", runs[1].StartLCN)
	}
}
