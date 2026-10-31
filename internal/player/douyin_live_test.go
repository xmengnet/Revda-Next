package player

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"revda/internal/config"
)

// TestLaunchDouyinE2E drives the dy- path end to end: streamfinder resolves
// the FLV URL, the danmaku client connects to the webcast wss endpoint, and
// mpv renders the resulting ASS track. Douyin live rooms churn quickly, so
// this only runs when DOUYIN_ROOM names a currently-live room:
//
//	DOUYIN_ROOM=271387799639 go test ./internal/player -run TestLaunchDouyinE2E -v
func TestLaunchDouyinE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	room := os.Getenv("DOUYIN_ROOM")
	if room == "" {
		t.Skip("set DOUYIN_ROOM to a live douyin web rid")
	}

	code := "dy-" + room
	ipcSock, subURL, _, err := launchDirect(code, config.DmliveConfig{
		DanmakuSpeed: 8000,
		FontAlpha:    0,
		FontScale:    1.0,
	}, false, []string{"--vo=null", "--ao=null"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { _ = mpvCommand(ipcSock, "quit") })
	if subURL == "" {
		t.Fatal("no danmaku sub URL for dy- room")
	}

	var mounted, titleOK bool
	var rendered []string
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		if !mounted {
			if data, err := mpvRequest(ipcSock, []any{"get_property", "track-list"}); err == nil {
				var tracks []struct {
					Type     string `json:"type"`
					External bool   `json:"external"`
				}
				if json.Unmarshal(data, &tracks) == nil {
					for _, tr := range tracks {
						if tr.Type == "sub" && tr.External {
							mounted = true
						}
					}
				}
			}
		}

		if !titleOK {
			if data, err := mpvRequest(ipcSock, []any{"get_property", "media-title"}); err == nil {
				var title string
				if json.Unmarshal(data, &title) == nil && title != "" &&
					!strings.Contains(title, "Douyin Live") {
					titleOK = true
					t.Logf("media-title: %s", title)
				}
			}
		}

		if len(rendered) < 3 {
			if data, err := mpvRequest(ipcSock, []any{"get_property", "sub-text"}); err == nil {
				var text string
				if json.Unmarshal(data, &text) == nil && strings.TrimSpace(text) != "" {
					rendered = append(rendered, text)
					t.Logf("sub-text: %q", text)
				}
			}
		}

		if mounted && titleOK && len(rendered) > 0 {
			return
		}
	}
	t.Fatalf("douyin pipeline incomplete: mounted=%v realTitle=%v rendered=%d", mounted, titleOK, len(rendered))
}
