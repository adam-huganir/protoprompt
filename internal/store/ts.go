package store

import (
	"sync"
	"time"
)

var (
	lastTSMu sync.Mutex
	lastTS   int64
)

// nextTS returns a strictly increasing unix-ms timestamp so that rows created
// within the same millisecond still have a deterministic creation order.
func nextTS() int64 {
	lastTSMu.Lock()
	defer lastTSMu.Unlock()
	now := time.Now().UnixMilli()
	if now <= lastTS {
		now = lastTS + 1
	}
	lastTS = now
	return now
}
