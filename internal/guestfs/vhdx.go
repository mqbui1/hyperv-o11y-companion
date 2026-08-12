// Package guestfs implements Tier 1.6 — read-only, host-side parsing of a
// guest's filesystem directly out of its VHDX file, with zero code running
// inside the guest. This exists because Linux guests have no equivalent of
// PowerShell Direct (Tier 1.5, internal/guestprobe is Windows-guest-only —
// see docs/phase3-guest-probe-plan.md's Non-goals), and this customer's
// no-in-guest-collector policy (confirmed to cover all guest OSes, not just
// Windows) rules out a guest-resident script or daemon as an alternative.
//
// vhdx.go is the container layer: VHDX (MS-VHDX) is Hyper-V's own disk
// image format — a 1MB header/metadata region followed by a Block
// Allocation Table (BAT) that maps logical disk offsets to physical file
// offsets, since dynamic VHDX files are sparse. Reader implements
// io.ReaderAt over the *logical* (virtual disk) address space so callers
// (gpt.go, xfs.go) never need to know whether the backing file is fixed or
// dynamic.
//
// v1 scope, deliberately limited (see docs/tier1.6-guestfs-probe-plan.md):
//   - Fixed and dynamic VHDX only. Differencing disks (checkpoints,
//     .avhdx parent chains) return ErrUnsupported — walking a parent
//     chain safely (including a VM with an *active* checkpoint, where the
//     leaf file is still being written) is real additional work deferred
//     out of v1.
//   - BAT blocks in PAYLOAD_BLOCK_FULLY_PRESENT state are read directly.
//     PAYLOAD_BLOCK_NOT_PRESENT / _UNMAPPED / _ZERO correctly return
//     zero-filled bytes (that's what an unallocated dynamic-VHDX block
//     means). PAYLOAD_BLOCK_PARTIALLY_PRESENT (sector-bitmap-tracked,
//     only possible with block sizes small enough to need one) returns
//     ErrUnsupported rather than guessing.
package guestfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
)

// ErrUnsupported is returned for any VHDX shape v1 deliberately doesn't
// handle (differencing disks, partially-present blocks) — callers should
// log and skip the VM, not retry.
var ErrUnsupported = errors.New("guestfs: unsupported VHDX layout")

const (
	identifierSignature = "vhdxfile"
	regionSignature     = 0x69676572         // "regi" little-endian uint32
	metadataSignature   = 0x617461646174656D // "metadata" little-endian uint64

	// Header region (offsets 64KB/128KB) is intentionally never read —
	// it only matters for log replay after an unclean shutdown, which is
	// irrelevant to read-only, best-effort export. Going straight to the
	// region table (below) is sufficient for everything this package does.
	regionOffset1 = 192 * 1024
	regionOffset2 = 256 * 1024

	regionEntryLen = 32
	metaEntryLen   = 32

	// BAT entry state, low 3 bits (MS-VHDX 2.3 "BAT entry state").
	batStateNotPresent       = 0
	batStateUndefined        = 1
	batStateZero             = 2
	batStateUnmapped         = 3
	batStateFullyPresent     = 6
	batStatePartiallyPresent = 7
)

// well-known metadata item GUIDs (MS-VHDX 3.5), as their raw 16-byte
// little-endian-encoded form matching how Go reads them off disk.
var (
	guidFileParameters  = mustGUID("caa16737-fa36-4d43-b3b6-33f0aa44e76b")
	guidVirtualDiskSize = mustGUID("2fa54224-cd1b-4876-b211-5dbed83bf4b8")
	guidRegionBAT       = mustGUID("2dc27766-f623-4200-9d64-115e9bfd4a08")
	guidRegionMetadata  = mustGUID("8b7ca206-4790-4b9a-b8fe-575f050f886e")
)

// guid is the raw 16-byte on-disk representation (little-endian first
// three fields, per Microsoft's GUID wire format).
type guid [16]byte

func mustGUID(s string) guid {
	g, err := parseGUID(s)
	if err != nil {
		panic(err) // only called with hardcoded, known-correct constants above
	}
	return g
}

func parseGUID(s string) (guid, error) {
	var g guid
	var d1 uint32
	var d2, d3 uint16
	var d4 [8]byte
	n, err := fmt.Sscanf(s, "%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		&d1, &d2, &d3, &d4[0], &d4[1], &d4[2], &d4[3], &d4[4], &d4[5], &d4[6], &d4[7])
	if err != nil || n != 11 {
		return g, fmt.Errorf("parsing guid %q: %w", s, err)
	}
	binary.LittleEndian.PutUint32(g[0:4], d1)
	binary.LittleEndian.PutUint16(g[4:6], d2)
	binary.LittleEndian.PutUint16(g[6:8], d3)
	copy(g[8:16], d4[:])
	return g, nil
}

// Reader is a read-only view of a VHDX's logical (virtual disk) address
// space. Safe for concurrent ReadAt calls (only holds an *os.File and
// immutable metadata parsed at Open time).
type Reader struct {
	f               *os.File
	blockSize       uint64 // File Parameters, bytes, power of 2 in [1MB,256MB]
	virtualDiskSize uint64
	hasParent       bool
	bat             []byte // raw BAT region bytes, indexed manually per block
}

// Open parses a VHDX's header/region/metadata structures and returns a
// Reader over its logical disk contents. Does not read any payload data
// yet — that happens lazily in ReadAt.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	r, err := newReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func (r *Reader) Close() error { return r.f.Close() }

// Size returns the virtual disk size in bytes (the guest-visible disk
// size, not the host file's on-disk size).
func (r *Reader) Size() int64 { return int64(r.virtualDiskSize) }

func newReader(f *os.File) (*Reader, error) {
	ident := make([]byte, len(identifierSignature))
	if _, err := f.ReadAt(ident, 0); err != nil {
		return nil, fmt.Errorf("reading identifier: %w", err)
	}
	if string(ident) != identifierSignature {
		return nil, fmt.Errorf("%w: not a VHDX file (bad identifier signature)", ErrUnsupported)
	}

	// Region table: try the primary copy, fall back to the backup — same
	// resilience model VHDX itself specifies for header/region pairs.
	region, err := readRegionTable(f, regionOffset1)
	if err != nil {
		region, err = readRegionTable(f, regionOffset2)
		if err != nil {
			return nil, fmt.Errorf("reading region table (both copies): %w", err)
		}
	}

	batEntry, ok := region[guidRegionBAT]
	if !ok {
		return nil, fmt.Errorf("%w: no BAT region entry", ErrUnsupported)
	}
	batOff, batLen := batEntry[0], batEntry[1]
	metaEntry, ok := region[guidRegionMetadata]
	if !ok {
		return nil, fmt.Errorf("%w: no metadata region entry", ErrUnsupported)
	}
	metaOff := metaEntry[0]

	blockSize, hasParent, err := readFileParameters(f, metaOff)
	if err != nil {
		return nil, err
	}
	if hasParent {
		return nil, fmt.Errorf("%w: differencing disk (has parent) — checkpoint/parent-chain VHDX not supported in v1", ErrUnsupported)
	}
	virtualDiskSize, err := readVirtualDiskSize(f, metaOff)
	if err != nil {
		return nil, err
	}

	bat := make([]byte, batLen)
	if _, err := f.ReadAt(bat, int64(batOff)); err != nil {
		return nil, fmt.Errorf("reading BAT region: %w", err)
	}

	return &Reader{f: f, blockSize: blockSize, virtualDiskSize: virtualDiskSize, hasParent: hasParent, bat: bat}, nil
}

// readRegionTable reads and validates one region-table copy (primary or
// backup) and returns a map of well-known region GUID -> (fileOffset, length).
func readRegionTable(f *os.File, at int64) (map[guid][2]uint64, error) {
	buf := make([]byte, 64*1024)
	if _, err := f.ReadAt(buf, at); err != nil {
		return nil, fmt.Errorf("reading region table at %#x: %w", at, err)
	}
	sig := binary.LittleEndian.Uint32(buf[0:4])
	if sig != regionSignature {
		return nil, fmt.Errorf("region table at %#x: bad signature", at)
	}
	if err := verifyChecksum(buf, 4, 64*1024); err != nil {
		return nil, fmt.Errorf("region table at %#x: %w", at, err)
	}
	entryCount := binary.LittleEndian.Uint32(buf[8:12])
	out := make(map[guid][2]uint64, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		off := 16 + i*regionEntryLen
		if int(off+regionEntryLen) > len(buf) {
			return nil, fmt.Errorf("region table at %#x: entry %d out of bounds", at, i)
		}
		var g guid
		copy(g[:], buf[off:off+16])
		fileOffset := binary.LittleEndian.Uint64(buf[off+16 : off+24])
		length := binary.LittleEndian.Uint32(buf[off+24 : off+28])
		out[g] = [2]uint64{fileOffset, uint64(length)}
	}
	return out, nil
}

// readFileParameters returns (BlockSize, HasParent) from the metadata
// region's required "File Parameters" item.
func readFileParameters(f *os.File, metaRegionOff uint64) (blockSize uint64, hasParent bool, err error) {
	itemOff, itemLen, err := findMetadataItem(f, metaRegionOff, guidFileParameters)
	if err != nil {
		return 0, false, err
	}
	if itemLen < 8 {
		return 0, false, fmt.Errorf("File Parameters item too short (%d bytes)", itemLen)
	}
	buf := make([]byte, 8)
	if _, err := f.ReadAt(buf, int64(metaRegionOff+itemOff)); err != nil {
		return 0, false, fmt.Errorf("reading File Parameters: %w", err)
	}
	blockSize = uint64(binary.LittleEndian.Uint32(buf[0:4]))
	flags := binary.LittleEndian.Uint32(buf[4:8])
	hasParent = flags&0x2 != 0 // bit 1: HasParent
	return blockSize, hasParent, nil
}

func readVirtualDiskSize(f *os.File, metaRegionOff uint64) (uint64, error) {
	itemOff, itemLen, err := findMetadataItem(f, metaRegionOff, guidVirtualDiskSize)
	if err != nil {
		return 0, err
	}
	if itemLen < 8 {
		return 0, fmt.Errorf("Virtual Disk Size item too short (%d bytes)", itemLen)
	}
	buf := make([]byte, 8)
	if _, err := f.ReadAt(buf, int64(metaRegionOff+itemOff)); err != nil {
		return 0, fmt.Errorf("reading Virtual Disk Size: %w", err)
	}
	return binary.LittleEndian.Uint64(buf), nil
}

// findMetadataItem scans the metadata region's item table for want and
// returns its (offset, length), both relative to metaRegionOff — matching
// how MS-VHDX defines metadata item offsets.
func findMetadataItem(f *os.File, metaRegionOff uint64, want guid) (offset, length uint64, err error) {
	header := make([]byte, 32)
	if _, err := f.ReadAt(header, int64(metaRegionOff)); err != nil {
		return 0, 0, fmt.Errorf("reading metadata header: %w", err)
	}
	sig := binary.LittleEndian.Uint64(header[0:8])
	if sig != metadataSignature {
		return 0, 0, fmt.Errorf("metadata region: bad signature")
	}
	entryCount := binary.LittleEndian.Uint16(header[10:12])
	entries := make([]byte, int(entryCount)*metaEntryLen)
	if _, err := f.ReadAt(entries, int64(metaRegionOff)+32); err != nil {
		return 0, 0, fmt.Errorf("reading metadata entries: %w", err)
	}
	for i := 0; i < int(entryCount); i++ {
		e := entries[i*metaEntryLen : (i+1)*metaEntryLen]
		var g guid
		copy(g[:], e[0:16])
		if g == want {
			off := binary.LittleEndian.Uint32(e[16:20])
			ln := binary.LittleEndian.Uint32(e[20:24])
			return uint64(off), uint64(ln), nil
		}
	}
	return 0, 0, fmt.Errorf("metadata item %x not found", want)
}

func verifyChecksum(buf []byte, checksumOff, structLen int) error {
	work := make([]byte, structLen)
	copy(work, buf[:structLen])
	binary.LittleEndian.PutUint32(work[checksumOff:checksumOff+4], 0)
	got := crc32.Checksum(work, crc32.MakeTable(crc32.Castagnoli))
	want := binary.LittleEndian.Uint32(buf[checksumOff : checksumOff+4])
	if got != want {
		return fmt.Errorf("checksum mismatch (got %#x, want %#x)", got, want)
	}
	return nil
}

// ReadAt implements io.ReaderAt over the logical (virtual disk) address
// space, resolving each requested byte range through the BAT one payload
// block at a time.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || uint64(off) >= r.virtualDiskSize {
		return 0, fmt.Errorf("guestfs: read offset %d out of range (disk size %d)", off, r.virtualDiskSize)
	}
	total := 0
	for total < len(p) {
		logicalOff := uint64(off) + uint64(total)
		if logicalOff >= r.virtualDiskSize {
			break
		}
		blockIdx := logicalOff / r.blockSize
		blockRelOff := logicalOff % r.blockSize
		n := len(p) - total
		if uint64(n) > r.blockSize-blockRelOff {
			n = int(r.blockSize - blockRelOff)
		}

		entryOff := blockIdx * 8
		if entryOff+8 > uint64(len(r.bat)) {
			return total, fmt.Errorf("guestfs: BAT index %d out of range", blockIdx)
		}
		entry := binary.LittleEndian.Uint64(r.bat[entryOff : entryOff+8])
		state := entry & 0x7
		fileOffsetMB := entry >> 20

		switch state {
		case batStateNotPresent, batStateUndefined, batStateZero, batStateUnmapped:
			for i := 0; i < n; i++ {
				p[total+i] = 0
			}
		case batStateFullyPresent:
			physOff := fileOffsetMB*1024*1024 + blockRelOff
			if _, err := r.f.ReadAt(p[total:total+n], int64(physOff)); err != nil {
				return total, fmt.Errorf("guestfs: reading payload block %d: %w", blockIdx, err)
			}
		case batStatePartiallyPresent:
			return total, fmt.Errorf("%w: block %d partially present (sector-bitmap tracked)", ErrUnsupported, blockIdx)
		default:
			return total, fmt.Errorf("%w: block %d unknown BAT state %d", ErrUnsupported, blockIdx, state)
		}
		total += n
	}
	return total, nil
}
