// Package processlock provides the small cross-process lock needed for
// non-database, read-modify-write state. It deliberately hides the bbolt file
// used as the locking primitive so callers do not mistake it for persistence.
package processlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.etcd.io/bbolt"
)

// Lock owns one exclusive process lock until Close.
type Lock struct {
	db *bbolt.DB
}

// Acquire opens an owned lock file and waits for at most maximum. A nearer
// context deadline shortens that wait. The file contains no application data.
func Acquire(ctx context.Context, path string, mode os.FileMode, maximum time.Duration) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maximum <= 0 {
		return nil, errors.New("process lock wait must be positive")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("process lock path must be a regular non-symlink file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect process lock path: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ctx.Err()
		}
		if remaining < maximum {
			maximum = remaining
		}
	}
	db, err := bbolt.Open(path, mode, &bbolt.Options{Timeout: maximum, NoFreelistSync: true})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Lock{db: db}, nil
}

// Close releases the process lock.
func (lock *Lock) Close() error {
	if lock == nil || lock.db == nil {
		return nil
	}
	err := lock.db.Close()
	lock.db = nil
	return err
}
