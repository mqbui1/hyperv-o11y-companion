package guestfs

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// encodeUTF16LE converts a plain ASCII/Unicode string to its UTF-16LE
// byte representation (2 bytes per character, no BOM).
func encodeUTF16LE(s string) []byte {
	buf := make([]byte, len(s)*2)
	for i, r := range s {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(r))
	}
	return buf
}

// buildDifferencingVHDX creates a minimal differencing VHDX whose Parent
// Locator metadata item points at parentPath via a "relative_path" key.
// All payload blocks are NOT_PRESENT — every read falls through to the parent.
// The virtual disk size matches the parent's layout (4 × 1MiB blocks).
func buildDifferencingVHDX(t *testing.T, parentPath string) string {
	t.Helper()
	const (
		blockSize = 1 * 1024 * 1024
		diskSize  = 4 * blockSize
		regionOff = 192 * 1024
		metaOff   = 1 * 1024 * 1024
		batOff    = 2 * 1024 * 1024
	)

	// Relative path from child directory to parent (same directory).
	relName := filepath.Base(parentPath)
	relNameUTF16 := encodeUTF16LE(relName)

	// Parent Locator item layout:
	//   bytes  0-15: LocatorType GUID (use guidParentLocator value itself)
	//   bytes 16-17: Reserved = 0
	//   bytes 18-19: KeyValueCount = 1
	//   bytes 20-31: Entry (KeyOffset, ValueOffset, KeyLength, ValueLength)
	//   bytes 32...: "relative_path" in UTF-16LE
	//   bytes 32+26: relName in UTF-16LE
	const keyStr = "relative_path"
	keyUTF16 := encodeUTF16LE(keyStr)
	keyOff := parentLocatorHeaderLen + parentLocatorEntryLen // 20 + 12 = 32
	valOff := keyOff + len(keyUTF16)
	parentLocatorItem := make([]byte, valOff+len(relNameUTF16))
	copy(parentLocatorItem[0:16], guidParentLocator[:])
	binary.LittleEndian.PutUint16(parentLocatorItem[18:20], 1) // KeyValueCount
	binary.LittleEndian.PutUint32(parentLocatorItem[20:24], uint32(keyOff))
	binary.LittleEndian.PutUint32(parentLocatorItem[24:28], uint32(valOff))
	binary.LittleEndian.PutUint16(parentLocatorItem[28:30], uint16(len(keyUTF16)))
	binary.LittleEndian.PutUint16(parentLocatorItem[30:32], uint16(len(relNameUTF16)))
	copy(parentLocatorItem[keyOff:], keyUTF16)
	copy(parentLocatorItem[valOff:], relNameUTF16)

	// Metadata region: header + 3 items (FileParams, VirtualDiskSize, ParentLocator).
	metaLen := 4096
	metaBuf := make([]byte, metaLen)
	binary.LittleEndian.PutUint64(metaBuf[0:8], metadataSignature)
	binary.LittleEndian.PutUint16(metaBuf[10:12], 3) // 3 entries

	// Item payloads placed after the entry table (3×32 = 96 bytes from offset 32).
	const (
		fileParamsOff   = 128 // well past 32+96=128; use 128 exactly
		vdiskSizeOff    = fileParamsOff + 8
		parentLocOff    = vdiskSizeOff + 8
	)
	writeMetaEntry(metaBuf[32:64], guidFileParameters, fileParamsOff, 8)
	writeMetaEntry(metaBuf[64:96], guidVirtualDiskSize, vdiskSizeOff, 8)
	writeMetaEntry(metaBuf[96:128], guidParentLocator, parentLocOff, uint32(len(parentLocatorItem)))
	// File Parameters: blockSize + HasParent flag (bit 1 = 0x2).
	binary.LittleEndian.PutUint32(metaBuf[fileParamsOff:fileParamsOff+4], blockSize)
	binary.LittleEndian.PutUint32(metaBuf[fileParamsOff+4:fileParamsOff+8], 0x2)
	binary.LittleEndian.PutUint64(metaBuf[vdiskSizeOff:vdiskSizeOff+8], diskSize)
	copy(metaBuf[parentLocOff:], parentLocatorItem)

	// BAT: 4 blocks, all NOT_PRESENT.
	batLen := 4 * 8
	batBuf := make([]byte, batLen)
	for i := 0; i < 4; i++ {
		writeBATEntry(batBuf[i*8:(i+1)*8], batStateNotPresent, 0)
	}

	// File layout:
	//   0: identifier
	//   regionOff: region table
	//   metaOff: metadata
	//   batOff: BAT
	fileSize := batOff + batLen
	buf := make([]byte, fileSize)
	copy(buf[0:], identifierSignature)

	regionBuf := buf[regionOff : regionOff+64*1024]
	binary.LittleEndian.PutUint32(regionBuf[0:4], regionSignature)
	binary.LittleEndian.PutUint32(regionBuf[8:12], 2)
	writeRegionEntry(regionBuf[16:48], guidRegionBAT, uint64(batOff), uint64(batLen))
	writeRegionEntry(regionBuf[48:80], guidRegionMetadata, uint64(metaOff), uint64(metaLen))
	fixChecksum(regionBuf, 4)

	copy(buf[metaOff:], metaBuf)
	copy(buf[batOff:], batBuf)

	childPath := filepath.Join(filepath.Dir(parentPath), "child.avhdx")
	if err := os.WriteFile(childPath, buf, 0o600); err != nil {
		t.Fatalf("writing differencing vhdx: %v", err)
	}
	return childPath
}

// TestResolveParentPath converts Windows-style relative backslash paths to
// host paths correctly.
func TestResolveParentPath_RelativeBackslash(t *testing.T) {
	child := "/mnt/vhds/checkpoints/vm.avhdx"
	rel := `.\vm.vhdx`
	got := resolveParentPath(child, rel)
	want := filepath.Join("/mnt/vhds/checkpoints", "vm.vhdx")
	if got != want {
		t.Fatalf("resolveParentPath = %q, want %q", got, want)
	}
}

func TestResolveParentPath_PlainName(t *testing.T) {
	child := "/srv/vms/snap.avhdx"
	rel := "base.vhdx"
	got := resolveParentPath(child, rel)
	want := "/srv/vms/base.vhdx"
	if got != want {
		t.Fatalf("resolveParentPath = %q, want %q", got, want)
	}
}

// TestEndToEnd_DifferencingDisk builds a parent VHDX with an XFS filesystem
// (75% used) and a differencing child where every block is NOT_PRESENT.
// Reading the child should produce the same 75% result by falling through
// to the parent for all blocks.
func TestEndToEnd_DifferencingDisk_FallsThroughToParent(t *testing.T) {
	parentPath := buildTestVHDX(t, false)
	childPath := buildDifferencingVHDX(t, parentPath)

	pct, err := FilesystemUsedPercent(childPath)
	if err != nil {
		t.Fatalf("FilesystemUsedPercent(differencing child): %v", err)
	}
	if pct != 75.0 {
		t.Errorf("used percent = %v, want 75.0 (from parent)", pct)
	}
}

// TestOpen_ChainCorrupt_MissingParent verifies that a differencing disk whose
// parent file does not exist returns ErrChainCorrupt.
func TestOpen_ChainCorrupt_MissingParent(t *testing.T) {
	// Build a child that points at a non-existent parent.
	dir := t.TempDir()
	fakeparent := filepath.Join(dir, "ghost.vhdx") // never created
	childPath := buildDifferencingVHDX(t, fakeparent)

	_, err := Open(childPath)
	if err == nil {
		t.Fatal("expected error opening child with missing parent, got nil")
	}
	if !errors.Is(err, ErrChainCorrupt) {
		t.Fatalf("Open(missing parent) err = %v, want ErrChainCorrupt", err)
	}
}
