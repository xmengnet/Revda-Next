package streamfinder

import (
	"context"
	"testing"
	"time"
)

func TestDouyuFinder(t *testing.T) {
	d := NewDouyuFinder()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 9999 or 475252
	info, err := d.Resolve(ctx, "https://www.douyu.com/9999")
	if err != nil {
		t.Logf("resolve douyu stream (streamer might be offline): %v", err)
		return
	}
	t.Logf("Resolved Douyu stream URL: %s", info.URL)
	if info.URL == "" {
		t.Errorf("expected non-empty stream URL")
	}
}
