// Package streamer downloads live streams (FLV/HLS) and relays the raw bytes
// onto local TCP sockets for ffmpeg to mux, mirroring Rust dmlive's
// streamer module.
package streamer

import (
	"sync"
	"sync/atomic"
	"time"
)

// MediaSegment is one HLS segment (or a youtube sq segment).
type MediaSegment struct {
	Skip int // 0: output, 1: download but do not output, 2: skip entirely
	// IsHeader marks the EXT-X-MAP init segment (youtube: the first sq).
	IsHeader bool
	URL      string
}

type segmentEntry struct {
	seg  MediaSegment
	sent bool
}

// SegmentStream deduplicates and schedules segments between the manifest
// refresher and the downloader, ported from Rust dmlive's segment.rs.
type SegmentStream struct {
	sequence    atomic.Uint64
	refreshItvl atomic.Uint64 // ms

	clipsMu sync.Mutex
	clips   []segmentEntry

	clipCh    chan MediaSegment
	refreshCh chan struct{}
}

func NewSegmentStream() *SegmentStream {
	ss := &SegmentStream{
		clipCh:    make(chan MediaSegment, 100),
		refreshCh: make(chan struct{}, 10),
	}
	ss.refreshItvl.Store(1000)
	return ss
}

// ClipCh yields downloadable segments in order.
func (ss *SegmentStream) ClipCh() <-chan MediaSegment { return ss.clipCh }

// RefreshCh is signalled whenever the manifest should be re-fetched.
func (ss *SegmentStream) RefreshCh() <-chan struct{} { return ss.refreshCh }

// RefreshIntervalMs is the current manifest refresh period.
func (ss *SegmentStream) RefreshIntervalMs() uint64 { return ss.refreshItvl.Load() }

// UpdateSequence merges a freshly parsed manifest into the pending queue.
// On the very first update only the last segment is emitted (older ones are
// marked skip so the stream starts live), matching Rust dmlive.
func (ss *SegmentStream) UpdateSequence(sq uint64, clips []MediaSegment, itvl uint64) {
	ss.refreshItvl.Store(itvl)

	ss.clipsMu.Lock()
	firstUpdate := len(ss.clips) == 0
	for i, clip := range clips {
		if ss.sequence.Load() < sq+uint64(i) {
			ss.sequence.Store(sq + uint64(i))
			ss.clips = append(ss.clips, segmentEntry{seg: clip})
		}
	}
	for len(ss.clips) > 15 {
		ss.clips = ss.clips[1:]
	}
	pending := make([]MediaSegment, 0, len(ss.clips))
	n := len(ss.clips)
	for i := range ss.clips {
		if ss.clips[i].sent {
			continue
		}
		ss.clips[i].sent = true
		clip := ss.clips[i].seg
		if firstUpdate && i < n-1 && clip.Skip == 0 {
			clip.Skip = 2
		}
		pending = append(pending, clip)
	}
	ss.clipsMu.Unlock()

	for _, clip := range pending {
		select {
		case ss.clipCh <- clip:
		default: // downloader stalled; drop the tail like a full channel would
		}
	}
}

// Run drives the refresh signal loop until its channel is closed. The loop
// re-arms with the current refresh interval and pauses when the sequence
// stalls (stream ended / manifest unchanged).
func (ss *SegmentStream) Run(stop <-chan struct{}) {
	lastSq := uint64(0)
	state := 0
	for {
		itvl := ss.refreshItvl.Load()
		if itvl == 0 {
			itvl = 1000
		}
		ticker := time.NewTicker(time.Duration(itvl) * time.Millisecond)
		<-ticker.C // immediate first tick, like tokio::interval
	inner:
		for {
			select {
			case <-stop:
				ticker.Stop()
				return
			case ss.refreshCh <- struct{}{}:
			}
			select {
			case <-stop:
				ticker.Stop()
				return
			case <-ticker.C:
			}
			if lastSq == ss.sequence.Load() {
				state = 1
				select {
				case <-stop:
					ticker.Stop()
					return
				case <-time.After(500 * time.Millisecond):
				}
				break inner
			}
			if state == 0 && ss.sequence.Load() != 0 {
				state = 1
				break inner
			}
			lastSq = ss.sequence.Load()
		}
		ticker.Stop()
	}
}
