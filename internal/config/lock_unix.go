//go:build !windows

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func acquireLock(ctx context.Context, path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockWithContext(ctx, f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func releaseLock(f *os.File) error {
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func lockWithContext(ctx context.Context, f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
