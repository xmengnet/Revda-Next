package streamer

import "testing"

func TestDecodeM3U8Basic(t *testing.T) {
	text := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:120
#EXTINF:6.000,
seg120.ts
#EXTINF:6.000,
seg121.ts
`
	m := DecodeM3U8(text)
	if m.sequence != 120 {
		t.Fatalf("sequence = %d, want 120", m.sequence)
	}
	if m.targetDuration != 6 {
		t.Fatalf("targetDuration = %d, want 6", m.targetDuration)
	}
	if len(m.clips) != 2 || m.clips[0].URL != "seg120.ts" || m.clips[1].URL != "seg121.ts" {
		t.Fatalf("clips = %+v", m.clips)
	}
	for _, c := range m.clips {
		if c.Skip != 0 || c.IsHeader {
			t.Fatalf("unexpected flags on %+v", c)
		}
	}
}

func TestDecodeM3U8AmazonAdsSkipped(t *testing.T) {
	text := `#EXT-X-MEDIA-SEQUENCE:5
#EXTINF:6.000,
real.ts
#EXTINF:30.000,Amazon
ad.ts
#EXTINF:6.000,
real2.ts
`
	m := DecodeM3U8(text)
	if len(m.clips) != 3 {
		t.Fatalf("clips = %+v", m.clips)
	}
	if m.clips[1].URL != "ad.ts" || m.clips[1].Skip != 1 {
		t.Fatalf("Amazon ad not marked skip=1: %+v", m.clips[1])
	}
	if m.clips[0].Skip != 0 || m.clips[2].Skip != 0 {
		t.Fatalf("regular segments misflagged: %+v", m.clips)
	}
}

func TestDecodeM3U8HeaderAndVariants(t *testing.T) {
	text := `#EXT-X-STREAM-INF:BANDWIDTH=1280000,RESOLUTION=640x360
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2560000,RESOLUTION=1920x1080
high/index.m3u8
`
	m := DecodeM3U8(text)
	if len(m.streams) != 2 {
		t.Fatalf("streams = %+v", m.streams)
	}
	if m.streams[1].bandwidth != 2560000 {
		t.Fatalf("bandwidth parse failed: %+v", m.streams)
	}
	if m.targetDuration != 1 || m.sequence != 0 {
		t.Fatalf("variant playlists must reset sequence/duration: %+v", m)
	}

	withMap := `#EXT-X-MAP:URI="init.mp4"
#EXTINF:4.000,
s0.m4s
`
	m2 := DecodeM3U8(withMap)
	if len(m2.clips) != 2 || !m2.clips[0].IsHeader || m2.clips[0].URL != "init.mp4" {
		t.Fatalf("EXT-X-MAP header segment missing: %+v", m2.clips)
	}
	if m2.clips[0].Skip != 1 {
		t.Fatalf("header segment should default to skip=1 (download, not output)")
	}
}

func TestSegmentStreamUpdateSequence(t *testing.T) {
	ss := NewSegmentStream()
	// First update: only the newest segment is emitted unskipped.
	ss.UpdateSequence(10, []MediaSegment{
		{URL: "a.ts"}, {URL: "b.ts"}, {URL: "c.ts"},
	}, 1000)
	if got := ss.sequence.Load(); got != 12 {
		t.Fatalf("sequence = %d, want 12", got)
	}
	var got []MediaSegment
	for len(got) < 3 {
		select {
		case c := <-ss.clipCh:
			got = append(got, c)
		default:
			goto done
		}
	}
done:
	if len(got) == 0 {
		t.Fatal("no segments emitted")
	}
	// First update emits everything but marks all but the last as skip=2.
	for _, c := range got[:len(got)-1] {
		if c.Skip != 2 {
			t.Fatalf("expected skip=2 on old segments, got %+v", c)
		}
	}
	if got[len(got)-1].URL != "c.ts" || got[len(got)-1].Skip != 0 {
		t.Fatalf("last segment must pass through: %+v", got[len(got)-1])
	}

	// Second update: only new segments are emitted, all unskipped.
	ss.UpdateSequence(13, []MediaSegment{{URL: "d.ts"}}, 1000)
	select {
	case c := <-ss.clipCh:
		if c.URL != "d.ts" || c.Skip != 0 {
			t.Fatalf("second update segment wrong: %+v", c)
		}
	default:
		t.Fatal("second update emitted nothing")
	}
}
