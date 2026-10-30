package player

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"revda/internal/config"
)

// TestLaunchDirectE2E drives the real launch path (launchDirect) end to end
// against live rooms and a regular video, with mpv forced headless so no
// window opens. It fails unless the danmaku track actually mounts and — for
// live rooms — something actually renders: the class of bug that unit tests
// on the WS clients alone cannot catch.
func TestLaunchDirectE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}

	cases := []struct {
		code     string
		wantText bool // live rooms must show text; video text depends on position
	}{
		{code: "do-9999", wantText: true},
		{code: "bi-6", wantText: true},
		{code: "BV1QWpS6JEx1", wantText: false},
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			ipcSock, subURL, _, err := launchDirect(tc.code, config.DmliveConfig{
				DanmakuSpeed: 8000,
				FontAlpha:    0,
				FontScale:    1.0,
			}, false, []string{"--vo=null", "--ao=null"})
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			t.Cleanup(func() { _ = mpvCommand(ipcSock, "quit") })

			if subURL != "" && !tc.wantText {
				// Static (video) danmaku: verify the document has content.
				resp, err := http.Get(subURL)
				if err != nil {
					t.Fatalf("fetch static danmaku: %v", err)
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
				resp.Body.Close()
				n := strings.Count(string(body), "Dialogue:")
				if n == 0 {
					t.Fatalf("static danmaku has no dialogue lines")
				}
				t.Logf("static danmaku: %d dialogue lines, %.1fKB", n, float64(len(body))/1024)
			}

			var mounted, titleOK, textOK bool
			deadline := time.Now().Add(90 * time.Second)
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
							!strings.Contains(title, "Bilibili Live") &&
							!strings.Contains(title, "Douyu Live") {
							titleOK = true
							t.Logf("media-title: %s", title)
						}
					}
				}

				if tc.wantText && !textOK {
					if data, err := mpvRequest(ipcSock, []any{"get_property", "sub-text"}); err == nil {
						var text string
						if json.Unmarshal(data, &text) == nil && strings.TrimSpace(text) != "" {
							textOK = true
							t.Logf("sub-text: %q", text)
						}
					}
				}

			needText := !tc.wantText || textOK
			if mounted && titleOK && needText {
				return
			}
			// A live stream that fails to start (bad CDN edge, cache stall)
			// leaves mpv alive with a title but nothing to mount against.
			// Detect that here so the failure names the real cause instead of
			// reporting the danmaku pipeline as broken.
			if pos, err := mpvGetProperty(ipcSock, "time-pos"); err == nil && pos < 0 {
				if idle, err := mpvGetProperty(ipcSock, "core-idle"); err == nil && idle > 0 {
					t.Fatalf("mpv stalled before playback started (time-pos<0, core-idle) — the stream itself failed to start, not the danmaku pipeline")
				}
			}
		}
		t.Fatalf("danmaku pipeline incomplete after 90s: mounted=%v realTitle=%v rendered=%v", mounted, titleOK, textOK)
		})
	}
}
