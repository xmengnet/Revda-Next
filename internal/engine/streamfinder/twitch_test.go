package streamfinder

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTwitchFinder(t *testing.T) {
	h := NewTwitchFinder()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	info, err := h.Resolve(ctx, "https://www.twitch.tv/loltyler1")
	if err != nil {
		t.Logf("resolve twitch error (streamer might be offline): %v", err)
		return
	}
	t.Logf("Resolved Twitch stream URL: %s", info.URL)
	if !strings.Contains(info.URL, "m3u8") {
		t.Errorf("expected an m3u8 variant URL, got %s", info.URL)
	}
}
