package streamfinder

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Live test against douyin: resolves a recommended live room and checks the
// returned pull URL actually serves FLV/HLS data. Override with DOUYIN_ROOM.
func TestDouyinFinderResolve(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	candidates := []string{os.Getenv("DOUYIN_ROOM")}
	explicit := candidates[0] != ""
	if !explicit {
		candidates = findLiveDouyinRooms(t)
	}

	var info *StreamInfo
	f := NewDouyinFinder()
	for _, room := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		got, err := f.Resolve(ctx, "https://live.douyin.com/"+room)
		cancel()
		if err != nil {
			t.Logf("room %s: %v", room, err)
			continue
		}
		info = got
		break
	}
	if info == nil {
		if !explicit {
			t.Skip("no discovered candidate room is live; set DOUYIN_ROOM env")
		}
		t.Fatal("no candidate room resolved to a live stream")
	}
	t.Logf("title=%q danmakuID=%q url=%s", info.Title, info.DanmakuID, info.URL)
	if info.URL == "" {
		t.Fatal("empty stream url")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", info.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range info.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream fetch failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream http status %d", resp.StatusCode)
	}
	head := make([]byte, 16)
	n, _ := io.ReadFull(resp.Body, head)
	head = head[:n]
	t.Logf("content-type=%q head=%q", resp.Header.Get("Content-Type"), head)

	if strings.Contains(info.URL, ".m3u8") {
		if !strings.HasPrefix(string(head), "#EXTM3U") {
			t.Fatalf("expected m3u8 payload, got %q", head)
		}
	} else if !strings.Contains(string(head), "FLV") {
		t.Fatalf("expected FLV magic, got %q", head)
	}
}

// findLiveDouyinRooms discovers live rooms through the similar_rooms list the
// enter API returns (the live homepage is client-rendered and carries almost
// no room ids in its SSR HTML).
func findLiveDouyinRooms(t *testing.T) []string {
	t.Helper()
	f := NewDouyinFinder()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, err := f.newSession(ctx)
	if err != nil {
		t.Skip("douyin session bootstrap failed: ", err)
	}
	resp, err := f.enterRoom(ctx, sess, "369324308707") // any room, live or not
	if err != nil {
		t.Skip("douyin seed room query failed: ", err)
	}
	var ids []string
	for _, sr := range resp.Data.SimilarRooms {
		if sr.WebRid != "" {
			ids = append(ids, sr.WebRid)
		}
	}
	if len(ids) == 0 {
		t.Skip("no similar rooms returned; set DOUYIN_ROOM env")
	}
	return ids
}
