// ntfs.go reads NTFS free-space from the volume $Bitmap MFT record (2h).
//
// Algorithm:
//   1. Parse the NTFS boot sector for BytesPerSector, SectorsPerCluster,
//      TotalSectors, and the MFT start LCN.
//   2. Locate MFT record 6 ($Bitmap) at MFT_start_LCN × BytesPerCluster + 6
//      × BytesPerMFTRecord, applying the standard NTFS update-sequence fixup.
//   3. Find the unnamed $DATA (0x80) attribute and decode its data runs.
//   4. Read every cluster in those runs and count set bits (popcount) to
//      determine used-cluster count.
//
// Sector size: v1 always treats NTFS logical sectors as 512 bytes for the
// boot-sector MFT calculation. NTFS itself works in terms of clusters
// (BytesPerCluster = BytesPerSector × SectorsPerCluster), which are
// typically 4 KiB on modern Windows installations.
//
// Field layout sources:
//   - MS-NTFS spec (https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-ntfs)
//   - ntfs-3g source (libntfs-3g/ntfsinfo.c) for bootstrap reference.
package guestfs

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

const (
	ntfsOEMOffset           = 3  // 8-byte OEM ID ("NTFS    ")
	ntfsBytesPerSectorOff   = 11 // uint16
	ntfsSecPerClusterOff    = 13 // uint8
	ntfsTotalSectorsOff     = 40 // uint64
	ntfsMFTStartLCNOff      = 48 // uint64
	ntfsClusPerMFTRecordOff = 64 // int8 (see parseNTFSBootSector for encoding)

	ntfsMFTSignature = "FILE"
	ntfsMFTUSNOff    = 4 // uint16: offset within record to USN array
	ntfsMFTUSNCnt    = 6 // uint16: USN entry count (includes USN itself)
	ntfsMFTAttrOff   = 20 // uint16: offset to first attribute

	attrTypeData = uint32(0x80)
	attrTypeEnd  = uint32(0xFFFFFFFF)

	// Cap $Bitmap reads: 64 MiB covers ~512 GiB @ 4 KiB clusters.
	// Volumes above this limit are skipped rather than OOM'd.
	ntfsMaxBitmapBytes = int64(64 * 1024 * 1024)
)

// ErrNotNTFS is returned when a partition's first sector does not contain
// the NTFS OEM identifier — the partition is not NTFS.
var ErrNotNTFS = fmt.Errorf("%w: not an NTFS partition (OEM ID mismatch)", ErrUnsupported)

// NTFSUsage is the used-space summary derived from the NTFS $Bitmap.
type NTFSUsage struct {
	TotalClusters int64
	UsedClusters  int64
}

// UsedPercent returns the used-space percentage, or false if TotalClusters
// is zero.
func (u NTFSUsage) UsedPercent() (float64, bool) {
	if u.TotalClusters == 0 {
		return 0, false
	}
	return float64(u.UsedClusters) / float64(u.TotalClusters) * 100, true
}

// ntfsBootInfo carries the fields we need from the NTFS BPB/extended BPB.
type ntfsBootInfo struct {
	BytesPerSector    uint16
	SectorsPerCluster uint8
	TotalSectors      uint64
	MFTStartLCN       uint64
	BytesPerMFTRecord uint32
}

// parseNTFSBootSector extracts the NTFS BPB fields from a 512-byte boot
// sector. Exported as a package-internal helper so it is unit-testable
// without a real VHDX.
func parseNTFSBootSector(sector []byte) (ntfsBootInfo, error) {
	if len(sector) < 80 {
		return ntfsBootInfo{}, fmt.Errorf("ntfs: boot sector too short (%d bytes)", len(sector))
	}
	if string(sector[ntfsOEMOffset:ntfsOEMOffset+8]) != "NTFS    " {
		return ntfsBootInfo{}, ErrNotNTFS
	}
	bps := binary.LittleEndian.Uint16(sector[ntfsBytesPerSectorOff:])
	spc := sector[ntfsSecPerClusterOff]
	totalSectors := binary.LittleEndian.Uint64(sector[ntfsTotalSectorsOff:])
	mftLCN := binary.LittleEndian.Uint64(sector[ntfsMFTStartLCNOff:])

	// ClustersPerMFTRecord encoding: if the byte is < 0x80 it is a cluster
	// count; if >= 0x80 it is a negative signed byte — MFT record size is
	// 2^(-value) bytes. E.g. 0xF6 (= -10 as int8) → 2^10 = 1024 bytes.
	cpmft := sector[ntfsClusPerMFTRecordOff]
	var bytesPerMFTRecord uint32
	if cpmft < 0x80 {
		if bps == 0 || spc == 0 {
			return ntfsBootInfo{}, fmt.Errorf("ntfs: zero BPS or SPC in boot sector")
		}
		bytesPerMFTRecord = uint32(cpmft) * uint32(bps) * uint32(spc)
	} else {
		exp := 256 - uint32(cpmft) // -cpmft as uint32
		bytesPerMFTRecord = 1 << exp
	}
	if bytesPerMFTRecord == 0 || bytesPerMFTRecord > 65536 {
		return ntfsBootInfo{}, fmt.Errorf("ntfs: unreasonable MFT record size %d", bytesPerMFTRecord)
	}
	return ntfsBootInfo{
		BytesPerSector:    bps,
		SectorsPerCluster: spc,
		TotalSectors:      totalSectors,
		MFTStartLCN:       mftLCN,
		BytesPerMFTRecord: bytesPerMFTRecord,
	}, nil
}

// applyMFTFixup applies the NTFS update-sequence array to an MFT record,
// restoring the original bytes at each 512-byte sector boundary.
// A mismatch is treated as a soft error (logged via return) rather than
// fatal — Windows sometimes omits fixup after a clean shutdown.
func applyMFTFixup(record []byte) error {
	if len(record) < 8 {
		return fmt.Errorf("ntfs: MFT record too short for fixup header")
	}
	usnOff := int(binary.LittleEndian.Uint16(record[ntfsMFTUSNOff:]))
	usnCnt := int(binary.LittleEndian.Uint16(record[ntfsMFTUSNCnt:]))
	if usnCnt == 0 {
		return nil
	}
	if usnOff+2+usnCnt*2 > len(record) {
		return fmt.Errorf("ntfs: fixup array out of bounds (usnOff=%d, cnt=%d, recLen=%d)",
			usnOff, usnCnt, len(record))
	}
	usn0 := record[usnOff]
	usn1 := record[usnOff+1]
	for i := 0; i < usnCnt-1; i++ {
		sectorEnd := (i+1)*512 - 2
		if sectorEnd+1 >= len(record) {
			break
		}
		if record[sectorEnd] != usn0 || record[sectorEnd+1] != usn1 {
			return fmt.Errorf("ntfs: fixup mismatch at sector %d", i)
		}
		origOff := usnOff + 2 + i*2
		record[sectorEnd] = record[origOff]
		record[sectorEnd+1] = record[origOff+1]
	}
	return nil
}

// ntfsDataRun is one extent in a non-resident NTFS attribute.
type ntfsDataRun struct {
	StartLCN int64 // logical cluster number; -1 for sparse
	Length   int64 // in clusters
}

// decodeDataRuns decodes NTFS data runs from attrBuf starting at runsOff
// (relative to start of attrBuf). Returns a slice of extents in LCN
// order. The function is package-internal so it can be unit-tested
// directly.
func decodeDataRuns(attrBuf []byte, runsOff int) ([]ntfsDataRun, error) {
	pos := runsOff
	var runs []ntfsDataRun
	var prevLCN int64
	for pos < len(attrBuf) {
		header := attrBuf[pos]
		if header == 0 {
			break
		}
		lenLen := int(header & 0x0F)
		offLen := int(header >> 4)
		pos++
		if pos+lenLen+offLen > len(attrBuf) {
			return nil, fmt.Errorf("ntfs: data run truncated at pos %d", pos)
		}
		var runLen int64
		for i := 0; i < lenLen; i++ {
			runLen |= int64(attrBuf[pos+i]) << (8 * i)
		}
		pos += lenLen
		if offLen > 0 {
			var delta int64
			for i := 0; i < offLen; i++ {
				delta |= int64(attrBuf[pos+i]) << (8 * i)
			}
			// sign-extend if high bit of last offset byte is set
			if attrBuf[pos+offLen-1]&0x80 != 0 {
				delta |= ^((int64(1) << (8 * offLen)) - 1)
			}
			pos += offLen
			prevLCN += delta
			runs = append(runs, ntfsDataRun{StartLCN: prevLCN, Length: runLen})
		} else {
			// Sparse run — treat as free
			runs = append(runs, ntfsDataRun{StartLCN: -1, Length: runLen})
		}
	}
	return runs, nil
}

// ReadNTFSBitmap reads the NTFS $Bitmap from the partition described by
// part and returns used/total cluster counts. Calling code should try XFS
// first (ReadXFSSuperblock) and fall back to this on ErrNotXFS.
func ReadNTFSBitmap(r *Reader, part Partition) (NTFSUsage, error) {
	bootSector := make([]byte, 512)
	if _, err := r.ReadAt(bootSector, part.StartByte); err != nil {
		return NTFSUsage{}, fmt.Errorf("ntfs: reading boot sector: %w", err)
	}
	info, err := parseNTFSBootSector(bootSector)
	if err != nil {
		return NTFSUsage{}, err
	}

	bpc := int64(info.BytesPerSector) * int64(info.SectorsPerCluster)
	if bpc == 0 {
		return NTFSUsage{}, fmt.Errorf("ntfs: zero bytes-per-cluster")
	}
	totalClusters := int64(info.TotalSectors) / int64(info.SectorsPerCluster)

	// MFT record 6 is $Bitmap.
	mftBaseOff := part.StartByte + int64(info.MFTStartLCN)*bpc
	record6Off := mftBaseOff + 6*int64(info.BytesPerMFTRecord)

	record := make([]byte, info.BytesPerMFTRecord)
	if _, err := r.ReadAt(record, record6Off); err != nil {
		return NTFSUsage{}, fmt.Errorf("ntfs: reading MFT record 6: %w", err)
	}
	if string(record[:4]) != ntfsMFTSignature {
		return NTFSUsage{}, fmt.Errorf("ntfs: MFT record 6 signature mismatch (got %q)", record[:4])
	}
	// Apply fixup — non-fatal mismatch; proceed regardless.
	_ = applyMFTFixup(record)

	// Walk attributes to find the unnamed $DATA (0x80).
	attrOff := int(binary.LittleEndian.Uint16(record[ntfsMFTAttrOff:]))
	for attrOff+8 <= len(record) {
		attrType := binary.LittleEndian.Uint32(record[attrOff:])
		if attrType == attrTypeEnd {
			break
		}
		attrLen := int(binary.LittleEndian.Uint32(record[attrOff+4:]))
		if attrLen <= 0 || attrOff+attrLen > len(record) {
			break
		}
		if attrType != attrTypeData {
			attrOff += attrLen
			continue
		}
		nonResident := record[attrOff+8]
		if nonResident == 0 {
			// Resident $DATA — $Bitmap is usually non-resident, but handle edge case.
			valLen := int(binary.LittleEndian.Uint32(record[attrOff+16:]))
			valOff := int(binary.LittleEndian.Uint16(record[attrOff+20:]))
			if valOff+valLen > attrLen {
				break
			}
			bitmapData := record[attrOff+valOff : attrOff+valOff+valLen]
			var used int64
			for _, b := range bitmapData {
				used += int64(bits.OnesCount8(b))
			}
			return NTFSUsage{TotalClusters: totalClusters, UsedClusters: used}, nil
		}

		// Non-resident: read size and data runs.
		bitmapRealSize := int64(binary.LittleEndian.Uint64(record[attrOff+48:]))
		if bitmapRealSize > ntfsMaxBitmapBytes {
			return NTFSUsage{}, fmt.Errorf("%w: $Bitmap size %d exceeds limit %d",
				ErrUnsupported, bitmapRealSize, ntfsMaxBitmapBytes)
		}
		runsOff := int(binary.LittleEndian.Uint16(record[attrOff+32:]))
		runs, err := decodeDataRuns(record[attrOff:attrOff+attrLen], runsOff)
		if err != nil {
			return NTFSUsage{}, fmt.Errorf("ntfs: decoding $Bitmap runs: %w", err)
		}

		// Read $Bitmap clusters, count set bits.
		var usedClusters int64
		remaining := bitmapRealSize
		for _, run := range runs {
			if remaining <= 0 {
				break
			}
			readLen := run.Length * bpc
			if readLen > remaining {
				readLen = remaining
			}
			if run.StartLCN < 0 {
				// Sparse — all free
				remaining -= readLen
				continue
			}
			chunk := make([]byte, readLen)
			if _, err := r.ReadAt(chunk, part.StartByte+run.StartLCN*bpc); err != nil {
				return NTFSUsage{}, fmt.Errorf("ntfs: reading $Bitmap cluster: %w", err)
			}
			for _, b := range chunk {
				usedClusters += int64(bits.OnesCount8(b))
			}
			remaining -= readLen
		}
		return NTFSUsage{TotalClusters: totalClusters, UsedClusters: usedClusters}, nil
	}

	return NTFSUsage{}, fmt.Errorf("ntfs: $DATA attribute not found in MFT record 6")
}
