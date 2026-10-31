package sites

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNormalizeInput(t *testing.T) {
	tests := []struct {
		input        string
		expectedCode string
		expectedURL  string
	}{
		{"bi-545342", "bi-545342", "https://live.bilibili.com/545342"},
		{"do-475252", "do-475252", "https://www.douyu.com/475252"},
		{"hu-660118", "hu-660118", "https://www.huya.com/660118"},
		{"yt-ch123", "yt-ch123", "https://www.youtube.com/@ch123"},
		{"ytv-v123", "ytv-v123", "https://www.youtube.com/watch?v=v123"},
		{"tw-ninja", "tw-ninja", "https://www.twitch.tv/ninja"},
		{"dy-271387799639", "dy-271387799639", "https://live.douyin.com/271387799639"},
		{"https://live.douyin.com/271387799639", "dy-271387799639", "https://live.douyin.com/271387799639"},
		{"BV1xx411c7mD", "BV1xx411c7mD", "https://www.bilibili.com/video/BV1xx411c7mD"},
		{"https://live.bilibili.com/545342", "bi-545342", "https://live.bilibili.com/545342"},
		{"https://www.douyu.com/475252", "do-475252", "https://www.douyu.com/475252"},
		{"https://www.huya.com/660118", "hu-660118", "https://www.huya.com/660118"},
	}

	for _, tt := range tests {
		code, playURL, err := NormalizeInput(tt.input)
		if err != nil {
			t.Errorf("NormalizeInput(%q) error: %v", tt.input, err)
			continue
		}
		if code != tt.expectedCode {
			t.Errorf("NormalizeInput(%q) code = %v, expected %v", tt.input, code, tt.expectedCode)
		}
		if playURL != tt.expectedURL {
			t.Errorf("NormalizeInput(%q) playURL = %v, expected %v", tt.input, playURL, tt.expectedURL)
		}
	}
}

func TestFetchLiveRooms(t *testing.T) {
	s := NewService()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test Bilibili room
	info, err := s.GetRoomInfo(ctx, "bi-545342")
	if err != nil {
		t.Logf("fetch bi-545342 warning: %v", err)
	} else {
		if info.RoomCode != "bi-545342" {
			t.Errorf("unexpected room code: %v", info.RoomCode)
		}
		if info.Title == "" && info.Owner == "" {
			t.Errorf("expected title or owner, got empty: %+v", info)
		}
		t.Logf("Bilibili room fetched: %+v", info)
	}

	// Test Douyu room
	info2, err := s.GetRoomInfo(ctx, "do-475252")
	if err != nil {
		t.Logf("fetch do-475252 warning: %v", err)
	} else {
		if info2.RoomCode != "do-475252" {
			t.Errorf("unexpected room code: %v", info2.RoomCode)
		}
		t.Logf("Douyu room fetched: %+v", info2)
	}
}

// TestDouyinRoomInfoLive exercises the room-list metadata path, which reuses
// the streamfinder enter API. Set DOUYIN_ROOM to a live web rid.
func TestDouyinRoomInfoLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	room := os.Getenv("DOUYIN_ROOM")
	if room == "" {
		t.Skip("set DOUYIN_ROOM to a douyin web rid")
	}
	s := NewService()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	info, err := s.GetRoomInfo(ctx, "dy-"+room)
	if err != nil {
		t.Fatalf("GetRoomInfo: %v", err)
	}
	t.Logf("code=%s title=%q owner=%q living=%v cover=%s", info.RoomCode, info.Title, info.Owner, info.IsLiving, info.Cover)
	if info.RoomCode != "dy-"+room {
		t.Fatalf("room code = %q", info.RoomCode)
	}
	if info.Title == "" || info.Owner == "" {
		t.Fatalf("empty title/owner: %+v", info)
	}
}
