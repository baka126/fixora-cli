//go:build windows

package hpafix

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func openPolicyFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("HPA policy must be a regular disk file")
	}
	return os.NewFile(uintptr(h), path), nil
}
