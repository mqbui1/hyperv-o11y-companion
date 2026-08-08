// Package creds reads named secrets from Windows Credential Manager, replacing
// the DPAPI-encrypted C:\ProgramData\O11yScripts\*.pw.txt files the current
// scheduled-task scripts use. One generic credential per secret:
//
//	cmdkey /generic:hyperv-o11y/scvmm       /user:example.com\svc-hyperv-o11y       /pass:<secret>
//	cmdkey /generic:hyperv-o11y/splunk-token /user:x-sf-token                        /pass:<access-token>
//
// Only the account the service runs as can read the credential back — same
// security property DPAPI gave the old scripts, without a plaintext-adjacent
// file on disk.
package creds

// Credential is a resolved Windows Credential Manager generic credential.
type Credential struct {
	Username string
	Password string
}

// Reader resolves a named credential. Implemented per-OS: credman_windows.go
// (real Credential Manager API) and credman_other.go (build/dev stub).
type Reader interface {
	Read(name string) (Credential, error)
}
