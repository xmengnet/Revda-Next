package streamfinder

import (
	"context"
	"testing"
	"time"
)

func TestHuyaFinder(t *testing.T) {
	h := NewHuyaFinder()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := h.Resolve(ctx, "https://www.huya.com/660118")
	if err != nil {
		t.Logf("resolve huya error (streamer might be offline): %v", err)
		return
	}
	t.Logf("Resolved Huya stream URL: %s", info.URL)
	if info.URL == "" {
		t.Errorf("expected non-empty stream URL")
	}
}
