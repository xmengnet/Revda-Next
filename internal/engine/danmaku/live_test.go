package danmaku

import (
	"context"
	"os"
	"testing"
	"time"
)

// liveDanmakuClient is the shared surface of the per-site live clients.
type liveDanmakuClient interface {
	Messages() <-chan *DanmakuMessage
	Start(context.Context) error
}

// runLiveDanmaku waits for one message from the first responsive room. An
// env override (when set) is the only candidate; otherwise def is tried
// followed by more. A broken auth/TLS chain fails because no candidate
// responds, while one quiet room no longer produces a false failure.
func runLiveDanmaku(t *testing.T, env, def string, more []string, newClient func(string) liveDanmakuClient) {
	t.Helper()
	rooms := []string{def}
	if v := os.Getenv(env); v != "" {
		rooms = []string{v}
	} else {
		rooms = append(rooms, more...)
	}

	const window = 20 * time.Second
	for _, room := range rooms {
		ctx, cancel := context.WithTimeout(context.Background(), window)
		client := newClient(room)
		go func() { _ = client.Start(ctx) }()
		select {
		case msg := <-client.Messages():
			cancel()
			t.Logf("room %s: [%s] %s (color %s)", room, msg.Nick, msg.Text, msg.Color)
			return
		case <-time.After(window):
			t.Logf("room %s: no danmaku in %s", room, window)
		}
		cancel()
	}
	t.Fatalf("no danmaku from candidate rooms %v (auth/TLS chain broken, or all rooms quiet)", rooms)
}

// Room 545068 (bilibili esports) and 7734200 carry steady chat; 6 (official
// radio) is only busy around events.
func TestBilibiliDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	runLiveDanmaku(t, "BILI_ROOM", "545068", []string{"7734200", "6"},
		func(room string) liveDanmakuClient { return NewBilibiliDanmakuClient(room) })
}

func TestDouyuDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	runLiveDanmaku(t, "DOUYU_ROOM", "1126960", []string{"12306", "9999"},
		func(room string) liveDanmakuClient { return NewDouyuDanmakuClient(room) })
}

// The huya register chain needs the mobile page ids (lYyid, lChannelId,
// lSubChannelId) plus a tars websocket register, so a broken codec or id
// extraction fails here. The previous default, 1995, no longer serves those
// ids at all; these rooms are the current top-heat ones.
func TestHuyaDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	runLiveDanmaku(t, "HUYA_ROOM", "998", []string{"189201", "528300"},
		func(room string) liveDanmakuClient { return NewHuyaDanmakuClient(room) })
}
