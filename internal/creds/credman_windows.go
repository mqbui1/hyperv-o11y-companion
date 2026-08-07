//go:build windows

package creds

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modAdvapi32          = syscall.NewLazyDLL("advapi32.dll")
	procCredRead         = modAdvapi32.NewProc("CredReadW")
	procCredFree         = modAdvapi32.NewProc("CredFree")
	credTypeGeneric      = uint32(1) // CRED_TYPE_GENERIC
)

// credential mirrors the fields of Win32 CREDENTIAL we actually use.
// Layout matches the subset of the real struct needed for CredentialBlob /
// CredentialBlobSize / UserName on amd64; full struct in wincred.h.
type win32Credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten         syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsReader struct{}

// NewReader returns a Windows Credential Manager-backed Reader.
func NewReader() Reader { return windowsReader{} }

func (windowsReader) Read(name string) (Credential, error) {
	targetPtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return Credential{}, err
	}
	var pCred *win32Credential
	ret, _, _ := procCredRead.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&pCred)),
	)
	if ret == 0 {
		return Credential{}, fmt.Errorf("CredRead(%q): %w (run cmdkey /generic:%q first)", name, syscall.GetLastError(), name)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(pCred)))

	var username string
	if pCred.UserName != nil {
		username = syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(pCred.UserName))[:])
	}
	password := ""
	if pCred.CredentialBlob != nil && pCred.CredentialBlobSize > 0 {
		blob := unsafe.Slice(pCred.CredentialBlob, int(pCred.CredentialBlobSize))
		// CredentialBlob is stored as UTF-16 for generic credentials written via cmdkey.
		u16 := make([]uint16, len(blob)/2)
		for i := range u16 {
			u16[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
		}
		password = syscall.UTF16ToString(u16)
	}
	return Credential{Username: username, Password: password}, nil
}
