package danmaku

import (
	"embed"
	"encoding/binary"
	"sync"
)

//go:embed mkv_header.mkv
var mkvHeaderFS embed.FS

// MKVHeader is the Segment/Tracks prologue of the live danmaku Matroska
// stream: two ASS subtitle tracks (scroll + fixed) whose codec private data
// carries the script header. Copied byte-for-byte from Rust dmlive.
var MKVHeader = func() []byte {
	b, err := mkvHeaderFS.ReadFile("mkv_header.mkv")
	if err != nil {
		panic("danmaku: embedded mkv_header.mkv missing: " + err.Error())
	}
	return b
}()

// dmKVBlockGroup wraps one ASS dialogue as a Matroska BlockGroup with a
// BlockDuration (the danmaku screen-crossing time).
type dmKVBlockGroup struct {
	blockGroupID       byte   // 0xa0
	blockGroupSize     uint32 // EBML "all ones" size marker style, matches Rust
	blockID            byte   // 0xa1
	blockSize          uint32
	blockTrackNumber   byte // 0x80 | track
	blockRelativeTime  uint16
	blockHeaderFlags   byte
	blockContent       []byte
	blockDurationID    byte   // 0x9b
	blockDurationSize  byte   // 0x84
	blockDurationValue uint32 // = danmaku_speed ms
}

// DMKVCluster accumulates ASS blocks for one Matroska Cluster and serializes
// them with the same byte layout as Rust dmlive's mkv_header.rs.
type DMKVCluster struct {
	mu          sync.Mutex
	timestamp   uint64
	clusterSize uint64
	danmaku     []dmKVBlockGroup
}

// NewDMKVCluster returns an empty cluster anchored at ts 0.
func NewDMKVCluster() *DMKVCluster {
	return &DMKVCluster{clusterSize: 10 | 0x0100_0000_0000_0000}
}

// Reset empties the cluster and re-anchors its base timestamp.
func (c *DMKVCluster) Reset(firstTS uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timestamp = firstTS
	c.danmaku = c.danmaku[:0]
	c.clusterSize = 10 | 0x0100_0000_0000_0000
}

// AddASSBlock appends one dialogue; ts is absolute stream ms and speed the
// duration in ms. ass must not be mutated afterwards.
func (c *DMKVCluster) AddASSBlock(ts uint64, ass []byte, speed uint64, track byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	assLen := len(ass)
	rel := ts - c.timestamp
	if ts < c.timestamp {
		rel = 0
	}
	c.danmaku = append(c.danmaku, dmKVBlockGroup{
		blockGroupID:       0xa0,
		blockGroupSize:     uint32(assLen+15) | 0x1000_0000,
		blockID:            0xa1,
		blockSize:          uint32(assLen+4) | 0x1000_0000,
		blockTrackNumber:   0x80 | track,
		blockRelativeTime:  uint16(rel), // truncates like Rust's `as u16`
		blockHeaderFlags:   0x00,
		blockContent:       ass,
		blockDurationID:    0x9b,
		blockDurationSize:  0x84,
		blockDurationValue: uint32(speed),
	})
	c.clusterSize += uint64(assLen) + 20
}

// WriteToBytes serializes the cluster (id + size + timestamp + blocks).
func (c *DMKVCluster) WriteToBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, 0, 128*1024)
	var u32 [4]byte
	var u64 [8]byte
	var u16 [2]byte

	binary.BigEndian.PutUint32(u32[:], 0x1f43b675)
	out = append(out, u32[:]...)
	binary.BigEndian.PutUint64(u64[:], c.clusterSize)
	out = append(out, u64[:]...)
	out = append(out, 0xe7, 0x88)
	binary.BigEndian.PutUint64(u64[:], c.timestamp)
	out = append(out, u64[:]...)

	for _, b := range c.danmaku {
		out = append(out, b.blockGroupID)
		binary.BigEndian.PutUint32(u32[:], b.blockGroupSize)
		out = append(out, u32[:]...)
		out = append(out, b.blockID)
		binary.BigEndian.PutUint32(u32[:], b.blockSize)
		out = append(out, u32[:]...)
		out = append(out, b.blockTrackNumber)
		binary.BigEndian.PutUint16(u16[:], b.blockRelativeTime)
		out = append(out, u16[:]...)
		out = append(out, b.blockHeaderFlags)
		out = append(out, b.blockContent...)
		out = append(out, b.blockDurationID, b.blockDurationSize)
		binary.BigEndian.PutUint32(u32[:], b.blockDurationValue)
		out = append(out, u32[:]...)
	}
	return out
}
