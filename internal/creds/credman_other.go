//go:build !windows

package creds

import (
	"fmt"
	"os"
)

// devReader is a non-Windows dev/test stand-in: reads
// HYPERV_O11Y_CRED_<NAME> env vars as "username:password" so the rest of the
// pipeline can be built/tested off-Windows. Never used in production —
// production always runs credman_windows.go on the Windows Service host.
type devReader struct{}

func NewReader() Reader { return devReader{} }

func (devReader) Read(name string) (Credential, error) {
	envName := "HYPERV_O11Y_CRED_" + sanitize(name)
	val := os.Getenv(envName)
	if val == "" {
		return Credential{}, fmt.Errorf("dev credential reader: set %s=user:pass (production uses Windows Credential Manager, see credman_windows.go)", envName)
	}
	for i := 0; i < len(val); i++ {
		if val[i] == ':' {
			return Credential{Username: val[:i], Password: val[i+1:]}, nil
		}
	}
	return Credential{}, fmt.Errorf("dev credential %s must be user:pass", envName)
}

func sanitize(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			out[i] = c
		} else {
			out[i] = '_'
		}
	}
	return string(out)
}
