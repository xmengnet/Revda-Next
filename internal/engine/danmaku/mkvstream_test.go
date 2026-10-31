package danmaku

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDMKVClusterLayout(t *testing.T) {
	c := NewDMKVCluster()
	c.Reset(0)
	// One block on track 1: absolute ts 1000, 4-byte ass payload, speed 8000.
	c.AddASSBlock(1000, []byte("test"), 8000, 1)
	data := c.WriteToBytes()

	// cluster id
	if binary.BigEndian.Uint32(data[0:4]) != 0x1f43b675 {
		t.Fatalf("cluster id = %x", data[0:4])
	}
	// timestamp id/size/value
	if data[12] != 0xe7 || data[13] != 0x88 {
		t.Fatalf("timestamp element header = %x %x", data[12], data[13])
	}
	if binary.BigEndian.Uint64(data[14:22]) != 0 {
		t.Fatalf("cluster timestamp = %d, want 0", binary.BigEndian.Uint64(data[14:22]))
	}

	// BlockGroup starts at 22: 0xa0, size, 0xa1, size, track, rel time, flags,
	// payload, 0x9b, 0x84, duration.
	off := 22
	if data[off] != 0xa0 {
		t.Fatalf("block group id = %x", data[off])
	}
	bgSize := binary.BigEndian.Uint32(data[off+1 : off+5])
	if bgSize != uint32(4+15)|0x1000_0000 {
		t.Fatalf("block group size = %x", bgSize)
	}
	off += 5
	if data[off] != 0xa1 {
		t.Fatalf("block id = %x", data[off])
	}
	blkSize := binary.BigEndian.Uint32(data[off+1 : off+5])
	if blkSize != uint32(4+4)|0x1000_0000 {
		t.Fatalf("block size = %x", blkSize)
	}
	off += 5
	if data[off] != 0x81 { // 0x80 | track 1
		t.Fatalf("track number = %x", data[off])
	}
	rel := binary.BigEndian.Uint16(data[off+1 : off+3])
	if rel != 1000 {
		t.Fatalf("relative time = %d, want 1000", rel)
	}
	if data[off+3] != 0x00 {
		t.Fatalf("block flags = %x", data[off+3])
	}
	if string(data[off+4:off+8]) != "test" {
		t.Fatalf("payload = %q", data[off+4:off+8])
	}
	off += 4 + 4
	if data[off] != 0x9b || data[off+1] != 0x84 {
		t.Fatalf("duration element header = %x %x", data[off], data[off+1])
	}
	if binary.BigEndian.Uint32(data[off+2:off+6]) != 8000 {
		t.Fatalf("block duration = %d", binary.BigEndian.Uint32(data[off+2:off+6]))
	}

	// Total size: header 22 + block group (5+5+1+2+1+4+2+4 = 24) = 46.
	if len(data) != 22+24 {
		t.Fatalf("cluster bytes = %d, want 46", len(data))
	}
}

func TestDMKVClusterRelativeTimeAfterReset(t *testing.T) {
	c := NewDMKVCluster()
	c.Reset(5000)
	c.AddASSBlock(6000, []byte("ab"), 8000, 2)
	data := c.WriteToBytes()
	if binary.BigEndian.Uint64(data[14:22]) != 5000 {
		t.Fatalf("cluster timestamp = %d, want 5000", binary.BigEndian.Uint64(data[14:22]))
	}
	off := 22 + 5 + 5
	if rel := binary.BigEndian.Uint16(data[off+1 : off+3]); rel != 1000 {
		t.Fatalf("relative time = %d, want 1000", rel)
	}
	if data[off] != 0x82 { // 0x80 | track 2
		t.Fatalf("track number = %x", data[off])
	}
}

func TestFudujiKiller(t *testing.T) {
	k := NewFudujiKiller()
	// First 21 repeats pass (Rust checks count>20 BEFORE incrementing);
	// the 22nd within the 3s window is suppressed.
	for i := 0; i < 21; i++ {
		if !k.DMCheck("spam") {
			t.Fatalf("iteration %d: early repeats must pass", i)
		}
	}
	if k.DMCheck("spam") {
		t.Fatal("22nd repeat within 3s must be suppressed")
	}
	if !k.DMCheck("other") {
		t.Fatal("different text must pass")
	}
}

func TestFormatASSTimeOfDay(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00:00.00"},
		{1500 * time.Millisecond, "0:00:01.50"},
		{61*time.Second + 230*time.Millisecond, "0:01:01.23"},
		{3661 * time.Second, "1:01:01.00"},
	}
	for _, c := range cases {
		if got := formatASSTimeOfDay(c.d); got != c.want {
			t.Errorf("formatASSTimeOfDay(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestLaneAllocator(t *testing.T) {
	s := NewDMSettings(8000, 1.0, 0)
	l := &laneState{}
	l.reset()

	// Fill all lanes: font 40 → channel_num = ceil(540/40) = 14.
	for i := 0; i < 14; i++ {
		if _, ok := l.getAvailable(s, 0, 100); !ok {
			t.Fatalf("lane %d should be free at pts 0", i)
		}
	}
	if _, ok := l.getAvailable(s, 0, 100); ok {
		t.Fatal("15th lane must be unavailable at pts 0")
	}
	// Much later, lanes free up again ((speed - cpts + begin) * s > 1920 no longer holds).
	if _, ok := l.getAvailable(s, 8000, 100); !ok {
		t.Fatal("lanes should recycle after the screen-crossing time")
	}
}

func TestDMSettingsDefaults(t *testing.T) {
	s := NewDMSettings(8000, 1.0, 0)
	if s.FontSize() != 40 {
		t.Fatalf("font size = %d", s.FontSize())
	}
	if s.ChannelNum() != 14 {
		t.Fatalf("channel num = %d", s.ChannelNum())
	}
	if got := s.BGRColor("1a2b3c"); got != "3c2b1a" {
		t.Fatalf("bgr swap = %q", got)
	}
	if got := s.AlphaHex(); got != "00" {
		t.Fatalf("alpha = %q", got)
	}
	s.FontAlpha.Store(1.0)
	if got := s.AlphaHex(); got != "ff" {
		t.Fatalf("alpha at 1.0 = %q", got)
	}
}

func TestServeStaticASSDocument(t *testing.T) {
	// End-to-end over a real loopback listener: ServeStaticASS must write a
	// header + sorted Dialogue lines to the first connection and close it.
	s := NewDMSettings(8000, 1.0, 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	msgs := []VideoDanmaku{
		{Offset: 2 * time.Second, Text: "second", Color: "ffffff"},
		{Offset: 1 * time.Second, Text: "first", Color: "1a2b3c"},
		{Offset: 3 * time.Second, Text: "top", Color: "", Top: true},
	}

	type result struct {
		data string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			done <- result{err: err}
			return
		}
		raw, rerr := io.ReadAll(c)
		done <- result{data: string(raw), err: rerr}
	}()

	go func() { _ = ServeStaticASS(context.Background(), ln, msgs, s) }()

	r := <-done
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	out := r.data
	if len(out) < len(assStreamHeader) || out[:20] != assStreamHeader[:20] {
		t.Fatal("ASS header missing")
	}
	want := "Dialogue: 0,0:00:01.00,0:00:09.00,Default,,0,0,0,,{\\alpha00\\fs40\\1c&3c2b1a&\\move(1920,0,"
	if !strings.Contains(out, want) {
		t.Fatalf("sorted first dialogue missing:\n%s", out)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") || !strings.Contains(out, "top") {
		t.Fatalf("messages missing:\n%s", out)
	}
	// Top danmaku maps to \an8 and must not allocate a scroll lane offset.
	if !strings.Contains(out, "\\an8}") {
		t.Fatalf("fixed danmaku missing \\an8:\n%s", out)
	}
}
