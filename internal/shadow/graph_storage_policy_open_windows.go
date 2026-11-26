//go:build windows

package shadow

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openGraphStoragePolicyFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		windows.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("graph storage policy must be a regular disk file")
	}
	return os.NewFile(uintptr(handle), path), nil
}
