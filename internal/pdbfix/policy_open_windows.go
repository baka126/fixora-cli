//go:build windows

package pdbfix

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func openPolicyFile(path string) (*os.File, error) {
	name, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return nil, e
	}
	h, e := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		return nil, e
	}
	kind, e := windows.GetFileType(h)
	if e != nil || kind != windows.FILE_TYPE_DISK {
		windows.CloseHandle(h)
		if e != nil {
			return nil, e
		}
		return nil, errors.New("policy must be a regular disk file")
	}
	return os.NewFile(uintptr(h), path), nil
}
