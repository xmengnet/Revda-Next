package danmaku

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration tests against live servers. Room 6 (bilibili official radio)
// and 9999 (douyu) have constant chat traffic; requiring at least one message
// in 60s makes a broken auth/TLS chain fail instead of silently passing.
// Override rooms via BILI_ROOM / DOUYU_ROOM env vars.
func TestBilibiliDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	room := os.Getenv("BILI_ROOM")
	if room == "" {
		room = "6"
	}
	client := NewBilibiliDanmakuClient(room)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	go func() { _ = client.Start(ctx) }()

	deadline := time.After(55 * time.Second)
	for {
		select {
		case msg := <-client.Messages():
			t.Logf("received: [%s] %s", msg.Nick, msg.Text)
			return
		case <-ctx.Done():
			t.Fatal("no danmaku within 60s (auth chain broken)")
		case <-deadline:
			t.Fatal("no danmaku within 55s")
		}
	}
}

func TestDouyuDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	room := os.Getenv("DOUYU_ROOM")
	if room == "" {
		room = "9999"
	}
	client := NewDouyuDanmakuClient(room)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	go func() { _ = client.Start(ctx) }()

	deadline := time.After(55 * time.Second)
	for {
		select {
		case msg := <-client.Messages():
			t.Logf("received: [%s] %s (color %s)", msg.Nick, msg.Text, msg.Color)
			return
		case <-ctx.Done():
			t.Fatal("no danmaku within 60s (TLS chain broken)")
		case <-deadline:
			t.Fatal("no danmaku within 55s")
		}
	}
}

// Room 1995 is a high-traffic huya room; the register chain needs the mobile
// page ids (lYyid, lChannelId, lSubChannelId) plus a tars websocket register,
// so a broken codec or id extraction fails here. Override via HUYA_ROOM.
func TestHuyaDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	room := os.Getenv("HUYA_ROOM")
	if room == "" {
		room = "1995"
	}
	client := NewHuyaDanmakuClient(room)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	go func() { _ = client.Start(ctx) }()

	deadline := time.After(55 * time.Second)
	for {
		select {
		case msg := <-client.Messages():
			t.Logf("received: [%s] %s (color %s)", msg.Nick, msg.Text, msg.Color)
			return
		case <-ctx.Done():
			t.Fatal("no danmaku within 60s (register/decode chain broken)")
		case <-deadline:
			t.Fatal("no danmaku within 55s")
		}
	}
}
