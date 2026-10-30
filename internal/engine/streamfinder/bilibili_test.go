package streamfinder

import (
	"context"
	"testing"
	"time"
)

func TestBilibiliFinder(t *testing.T) {
	b := NewBilibiliFinder("")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Room 6 is Bilibili 24/7 official live room
	info, err := b.Resolve(ctx, "https://live.bilibili.com/6")
	if err != nil {
		t.Logf("resolve live error (might be offline or rate-limited): %v", err)
		return
	}
	t.Logf("Resolved Bilibili stream URL: %s", info.URL)
	if info.URL == "" {
		t.Errorf("expected non-empty stream URL")
	}
}
