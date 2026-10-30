package streamfinder

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Tries the requester's channel first; falls back to a 24/7 live channel so
// the test still exercises the live path when bennybennion1 is offline.
func TestYoutubeFinder(t *testing.T) {
	y := NewYoutubeFinder()
	candidates := []string{
		"https://www.youtube.com/@bennybennion1/live",
		"https://www.youtube.com/@LofiGirl/live",
	}
	for _, u := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		info, err := y.Resolve(ctx, u)
		cancel()
		if err != nil {
			t.Logf("resolve %s error: %v", u, err)
			continue
		}
		t.Logf("Resolved YouTube video URL (%d bytes) + audio URL (%d bytes), title: %s",
			len(info.URL), len(info.AudioURL), info.Title)
		if !strings.HasPrefix(info.URL, "https://") || !strings.HasPrefix(info.AudioURL, "https://") {
			t.Errorf("expected https video+audio URLs")
		}
		return
	}
	t.Skip("no candidate channel is live right now")
}
