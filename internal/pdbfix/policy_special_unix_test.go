//go:build !windows

package pdbfix

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReviewFIFOPolicyDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approval.yaml")
	if e := syscall.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := LoadPolicy(path); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("policy loader blocked before checking regular-file type")
		go func() {
			f, e := os.OpenFile(path, os.O_WRONLY, 0)
			if e == nil {
				f.Close()
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("FIFO loader failed to unblock")
		}
	}
}

func TestReviewApprovalRecheckRejectsSpecialReplacement(t *testing.T) {
	a, e := loadText(t, validPolicy)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(a.path); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Mkfifo(a.path, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := a.ProposalContext("team", "protect"); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("special replacement retained approval")
		}
	case <-time.After(time.Second):
		go func() {
			f, e := os.OpenFile(a.path, os.O_WRONLY, 0)
			if e == nil {
				f.Close()
			}
		}()
		t.Fatal("approval recheck blocked on replacement FIFO")
	}
}
func TestReviewPolicyOpenCannotBlockOrFollowReplacement(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy")
			if kind == "fifo" {
				if e := syscall.Mkfifo(path, 0600); e != nil {
					t.Fatal(e)
				}
			} else {
				target := filepath.Join(t.TempDir(), "target")
				os.WriteFile(target, []byte("must not follow"), 0600)
				if e := os.Symlink(target, path); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				f, e := openPolicyFile(path)
				if f != nil {
					f.Close()
				}
				done <- e
			}()
			select {
			case e := <-done:
				if kind == "symlink" && e == nil {
					t.Fatal("followed replacement symlink")
				}
			case <-time.After(time.Second):
				go func() {
					f, e := os.OpenFile(path, os.O_WRONLY, 0)
					if e == nil {
						f.Close()
					}
				}()
				t.Fatal("replacement FIFO blocked open")
			}
		})
	}
}
