// probe.go is the public entrypoint for Tier 1.6: given a VHDX path, open
// it, walk its GPT, find the root filesystem partition, and parse its
// superblock — entirely read-only, entirely from the host, with zero
// guest interaction of any kind.
package guestfs

import "fmt"

// FilesystemUsedPercent opens the VHDX at path, locates its root Linux
// filesystem partition, and returns that filesystem's used-space
// percentage. Returns ErrUnsupported (or a wrapped variant: ErrLVM,
// ErrNotXFS) for any v1-unsupported layout — callers should log and skip
// that VM's probe cycle, not treat it as a fatal error.
func FilesystemUsedPercent(path string) (float64, error) {
	r, err := Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening vhdx: %w", err)
	}
	defer r.Close()

	parts, err := ReadPartitionTable(r)
	if err != nil {
		return 0, fmt.Errorf("reading partition table: %w", err)
	}
	root, err := FindRootFilesystem(parts)
	if err != nil {
		return 0, err
	}
	usage, err := ReadXFSSuperblock(r, root)
	if err != nil {
		return 0, err
	}
	pct, ok := usage.UsedPercent()
	if !ok {
		return 0, fmt.Errorf("guestfs: zero-size filesystem")
	}
	return pct, nil
}
