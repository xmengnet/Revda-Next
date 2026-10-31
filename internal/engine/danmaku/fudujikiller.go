package danmaku

import (
	"hash/fnv"
	"sync"
	"time"
)

// FudujiKiller suppresses danmaku spam ("复读机"): the same text repeated
// more than 20 times within 3 seconds is dropped, matching Rust dmlive.
type FudujiKiller struct {
	mu        sync.Mutex
	startTime time.Time
	dmStats   map[uint64]*fudujikillerEntry
}

type fudujikillerEntry struct {
	lastMS int64
	count  int64
}

func NewFudujiKiller() *FudujiKiller {
	return &FudujiKiller{
		startTime: time.Now(),
		dmStats:   make(map[uint64]*fudujikillerEntry),
	}
}

// DMCheck reports whether the text may pass through.
func (k *FudujiKiller) DMCheck(dm string) bool {
	h := fnv.New64a()
	_, _ = h.Write([]byte(dm))
	dmHash := h.Sum64()

	k.mu.Lock()
	defer k.mu.Unlock()
	ret := true
	now := time.Since(k.startTime).Milliseconds()
	if it, ok := k.dmStats[dmHash]; ok {
		if now > it.lastMS+3000 {
			it.lastMS = now
			if it.count > 0 {
				it.count--
			}
		} else if it.count > 20 {
			ret = false
			it.count++
		} else {
			it.count++
		}
	} else {
		k.dmStats[dmHash] = &fudujikillerEntry{lastMS: now, count: 1}
	}
	if len(k.dmStats) > 30 {
		for hash, v := range k.dmStats {
			if v.count < 5 || now > v.lastMS+20_000 {
				delete(k.dmStats, hash)
			}
		}
	}
	return ret
}
