package player

import (
	"encoding/json"
	"testing"
	"time"

	"revda/internal/config"
)

// TestBangumiLaunchE2E drives the real launch path for a bangumi episode and
// a season-with-index form, and requires the danmaku track to mount and
// render text.
func TestBangumiLaunchE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	for _, code := range []string{"ep284092", "ss28533:2"} {
		t.Run(code, func(t *testing.T) {
			ipcSock, subURL, _, err := launchDirect(code, config.DmliveConfig{
				DanmakuSpeed: 8000, FontAlpha: 0, FontScale: 1.0,
			}, false, []string{"--vo=null", "--ao=null"})
			if err != nil {
				t.Fatalf("launch %s: %v", code, err)
			}
			t.Cleanup(func() { _ = mpvCommand(ipcSock, "quit") })
			if subURL == "" {
				t.Fatalf("%s: no danmaku sub url", code)
			}

			deadline := time.Now().Add(90 * time.Second)
			var mounted, textOK bool
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
				if !textOK {
					if txt, err := mpvGetString(ipcSock, "sub-text"); err == nil && txt != "" {
						textOK = true
						t.Logf("%s sub-text: %q", code, txt)
					}
				}
				if mounted && textOK {
					return
				}
			}
			t.Fatalf("%s: mounted=%v text=%v", code, mounted, textOK)
		})
	}
}
