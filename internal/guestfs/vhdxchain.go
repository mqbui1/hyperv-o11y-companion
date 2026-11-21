// vhdxchain.go — parent-chain resolution for differencing (snapshot) VHDXs (2f).
//
// A differencing VHDX (.avhdx) created by a Hyper-V checkpoint or New-VHD
// -Differencing stores a "Parent Locator" metadata item that points at its
// base disk. Blocks present in the child BAT (FULLY_PRESENT) override the
// parent; blocks with state NOT_PRESENT fall through to the parent — giving a
// merged logical-disk view that matches what the guest OS sees.
//
// Chain resolution is recursive: each parent may itself be a differencing disk.
// A depth cap (maxChainDepth) prevents infinite loops on corrupt data.
package guestfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxChainDepth = 32 // generous; real Hyper-V checkpoint chains are rarely >10 deep

var (
	// ErrChainCorrupt is returned when a differencing disk's parent chain
	// cannot be followed: parent file missing, unreadable, or not a valid VHDX.
	// Maps to skip_reason=chain_corrupt in ClassifySkipReason.
	ErrChainCorrupt = fmt.Errorf("%w: differencing disk parent chain corrupt or inaccessible", ErrUnsupported)

	// ErrChainModified is returned when the parent VHDX was written to after
	// the differencing child was created — the child's view of the base disk
	// no longer matches what is on disk. Data reads would return incoherent
	// merged content. Maps to skip_reason=chain_modified in ClassifySkipReason.
	ErrChainModified = fmt.Errorf("%w: differencing disk parent was modified after child snapshot was taken", ErrUnsupported)

	// guidParentLocator is the well-known metadata item GUID for the Parent
	// Locator structure (MS-VHDX §3.5.5).
	guidParentLocator = mustGUID("a8d35f2d-b30b-454d-abf7-d3d84834ab0c")
)

const (
	parentLocatorHeaderLen = 20 // LocatorType(16) + Reserved(2) + KeyValueCount(2)
	parentLocatorEntryLen  = 12 // KeyOffset(4) + ValueOffset(4) + KeyLength(2) + ValueLength(2)
)

// openParentChain reads the Parent Locator metadata item from child,
// resolves the parent VHDX path relative to the child file's location, and
// opens it. depth tracks recursion depth to enforce maxChainDepth.
func openParentChain(child *os.File, metaRegionOff uint64, depth int) (*Reader, error) {
	if depth >= maxChainDepth {
		return nil, fmt.Errorf("%w: chain depth exceeds %d", ErrChainCorrupt, maxChainDepth)
	}
	relPath, err := readParentLocatorPath(child, metaRegionOff)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChainCorrupt, err)
	}
	parentPath := resolveParentPath(child.Name(), relPath)
	pf, err := os.Open(parentPath)
	if err != nil {
		return nil, fmt.Errorf("%w: opening parent %s: %v", ErrChainCorrupt, parentPath, err)
	}
	pr, err := newReader(pf, depth+1)
	if err != nil {
		pf.Close()
		// Preserve chain-specific sentinels that propagated from deeper levels.
		if errors.Is(err, ErrChainCorrupt) || errors.Is(err, ErrChainModified) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrChainCorrupt, err)
	}
	return pr, nil
}

// resolveParentPath builds the host-filesystem path for the parent VHDX.
// childPath is the child VHDX's absolute (or relative) path as returned by
// os.File.Name(); relPath is the value extracted from the Parent Locator
// metadata entry (UTF-16LE key "relative_path", possibly Windows backslashes).
func resolveParentPath(childPath, relPath string) string {
	// Normalise Windows path separators to the host's separator.
	relPath = strings.ReplaceAll(relPath, "\\", string(filepath.Separator))
	return filepath.Join(filepath.Dir(childPath), relPath)
}

// readParentLocatorPath extracts the first usable path from the child's
// Parent Locator metadata item. Keys are tried in preference order:
// "relative_path" (most portable across move/rename operations), then
// "absolute_win32_path" as a fallback for locally-generated checkpoints.
func readParentLocatorPath(f *os.File, metaRegionOff uint64) (string, error) {
	itemOff, itemLen, err := findMetadataItem(f, metaRegionOff, guidParentLocator)
	if err != nil {
		return "", fmt.Errorf("parent locator item not found: %w", err)
	}
	if itemLen < uint64(parentLocatorHeaderLen) {
		return "", fmt.Errorf("parent locator item too short (%d bytes)", itemLen)
	}

	buf := make([]byte, itemLen)
	if _, err := f.ReadAt(buf, int64(metaRegionOff+itemOff)); err != nil {
		return "", fmt.Errorf("reading parent locator: %w", err)
	}

	kvCount := int(binary.LittleEndian.Uint16(buf[18:20]))
	if parentLocatorHeaderLen+kvCount*parentLocatorEntryLen > int(itemLen) {
		return "", fmt.Errorf("parent locator: entry count %d exceeds item size %d", kvCount, itemLen)
	}

	// Decode all key-value pairs into a map.
	paths := make(map[string]string, kvCount)
	for i := 0; i < kvCount; i++ {
		eOff := parentLocatorHeaderLen + i*parentLocatorEntryLen
		e := buf[eOff : eOff+parentLocatorEntryLen]

		keyOff := int(binary.LittleEndian.Uint32(e[0:4]))
		valOff := int(binary.LittleEndian.Uint32(e[4:8]))
		keyLen := int(binary.LittleEndian.Uint16(e[8:10]))
		valLen := int(binary.LittleEndian.Uint16(e[10:12]))

		if keyOff+keyLen > int(itemLen) || valOff+valLen > int(itemLen) {
			continue // malformed entry; skip
		}
		key := decodeUTF16LE(buf[keyOff : keyOff+keyLen])
		val := decodeUTF16LE(buf[valOff : valOff+valLen])
		paths[key] = val
	}

	for _, key := range []string{"relative_path", "absolute_win32_path", "volume_path"} {
		if p, ok := paths[key]; ok && p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("parent locator: no usable path key found in %d entries", kvCount)
}
