package guestfs

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifySkipReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want SkipReason
	}{
		{"nil", nil, ""},
		{"differencing", ErrDifferencingDisk, SkipReasonDifferencing},
		{"chain_corrupt", ErrChainCorrupt, SkipReasonChainCorrupt},
		{"chain_modified", ErrChainModified, SkipReasonChainModified},
		{"wrapped chain_corrupt", fmt.Errorf("probe: %w", ErrChainCorrupt), SkipReasonChainCorrupt},
		{"lvm", ErrLVM, SkipReasonLVM},
		{"mbr", ErrNotMBR, SkipReasonMBR},
		{"luks", ErrLUKS, SkipReasonEncrypted},
		{"not xfs", ErrNotXFS, SkipReasonOtherFS},
		{"no linux partition", ErrNoLinuxPartition, SkipReasonNoLinuxPart},
		{"wrapped differencing", fmt.Errorf("probe: %w", ErrDifferencingDisk), SkipReasonDifferencing},
		{"unrecognized", errors.New("boom"), SkipReasonOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySkipReason(tc.err); got != tc.want {
				t.Fatalf("ClassifySkipReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
