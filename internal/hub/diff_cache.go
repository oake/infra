package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
	"time"
)

var engines = struct {
	sync.Mutex
	entries map[string]engineFingerprint
}{entries: map[string]engineFingerprint{}}

type engineFingerprint struct {
	size     int64
	modified time.Time
	hash     string
}

func fingerprint(binary string) (string, error) {
	info, err := os.Stat(binary)
	if err != nil {
		return "", err
	}
	engines.Lock()
	defer engines.Unlock()
	old := engines.entries[binary]
	if old.size == info.Size() && old.modified.Equal(info.ModTime()) {
		return old.hash, nil
	}
	f, err := os.Open(binary)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	hash.Write([]byte{0})
	value := hex.EncodeToString(hash.Sum(nil))
	engines.entries[binary] = engineFingerprint{info.Size(), info.ModTime(), value}
	return value, nil
}

var diffWork = struct {
	sync.Mutex
	active map[string]chan struct{}
}{active: map[string]chan struct{}{}}

// Concurrent requests for one path pair share the persisted result. Independent
// pairs may still run in parallel. Completed keys leave no in-memory bookkeeping.
func lockDiff(ctx context.Context, key string) (func(), error) {
	for {
		diffWork.Lock()
		pending, exists := diffWork.active[key]
		if !exists {
			pending = make(chan struct{})
			diffWork.active[key] = pending
			diffWork.Unlock()
			return func() { diffWork.Lock(); delete(diffWork.active, key); close(pending); diffWork.Unlock() }, nil
		}
		diffWork.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending:
		}
	}
}
