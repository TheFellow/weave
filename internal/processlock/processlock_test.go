package processlock

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAcquireSerializesAndHonorsContextDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	first, err := Acquire(context.Background(), path, 0o600, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	second, err := Acquire(ctx, path, 0o600, time.Second)
	if second != nil {
		_ = second.Close()
	}
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("contended acquire = %v after %v", err, time.Since(started))
	}
}

func TestAcquireRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "state.lock")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if lock, err := Acquire(context.Background(), symlink, 0o600, time.Second); err == nil {
		_ = lock.Close()
		t.Fatal("symlinked process lock was accepted")
	}
}
