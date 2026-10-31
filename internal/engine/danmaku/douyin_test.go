package danmaku

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live test against douyin: connects to the webcast im push wss endpoint and
// requires at least one chat message. The default room is a small channel —
// override with DOUYIN_ROOM (public web rid from live.douyin.com/{rid}) for a
// busier room.
func TestDouyinDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	room := os.Getenv("DOUYIN_ROOM")
	if room == "" {
		room = "271387799639"
	}
	client := NewDouyinDanmakuClient(room)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	go func() { _ = client.Start(ctx) }()

	deadline := time.After(85 * time.Second)
	for {
		select {
		case msg := <-client.Messages():
			t.Logf("received: [%s] %s", msg.Nick, msg.Text)
			return
		case <-ctx.Done():
			t.Fatal("no danmaku within 90s (signature/wss chain broken)")
		case <-deadline:
			t.Fatal("no danmaku within 85s")
		}
	}
}
