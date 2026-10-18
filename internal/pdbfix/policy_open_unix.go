//go:build !windows

package pdbfix

import (
	"golang.org/x/sys/unix"
	"os"
)

// A replacement FIFO must not block, and a replacement symlink must not be followed.
func openPolicyFile(path string) (*os.File, error) {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), path), nil
}
