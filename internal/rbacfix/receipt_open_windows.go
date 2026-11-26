//go:build windows

package rbacfix

import "os"

func openReceipt(path string) (*os.File, error) { return os.Open(path) }
