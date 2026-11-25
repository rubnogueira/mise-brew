package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// prefixLock is mise's own lock (var/homebrew/locks/mise-bootstrap.lock). mise
// takes it with flock(2) while it links kegs, so holding it keeps mise-brew's
// direct prefix edits (link, unlink, uninstall, cleanup) from racing a mise run.
// It must never be held while mise-brew itself runs mise, or both would wait.
type prefixLock struct{ f *os.File }

var heldLock *prefixLock

func lockPrefix() func() {
	if heldLock != nil {
		return func() {}
	}
	dir := filepath.Join(brewPrefix(), "var", "homebrew", "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "mise-bootstrap.lock"), os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		ohai("Waiting for another mise or brew process to finish...")
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return func() {}
		}
	}
	heldLock = &prefixLock{f}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		heldLock = nil
	}
}
