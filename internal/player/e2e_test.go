package player

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"revda/internal/config"
	"revda/internal/engine/streamfinder"
)

// Headless end-to-end: resolve a live channel, launch the same mpv command
// line the app uses (forced to null outputs), then require playback to start
// (time-pos > 0). Run with REVDA_E2E=1 go test ./internal/player -run E2E -v.
func requirePlayback(t *testing.T, roomCode string) {
	t.Helper()
	if os.Getenv("REVDA_E2E") == "" || testing.Short() {
		t.Skip("e2e test; set REVDA_E2E=1 to run")
	}
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}

	sock, _, _, err := launchDirect(roomCode, config.DmliveConfig{}, false, []string{"--vo=null", "--ao=null", "--really-quiet"})
	if err != nil {
		t.Fatalf("launchDirect(%s): %v", roomCode, err)
	}

	deadline := time.Now().Add(75 * time.Second)
	playing := false
	for time.Now().Before(deadline) {
		pos, err := mpvGetProperty(sock, "time-pos")
		if err == nil && pos > 0 {
			playing = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	_ = mpvCommand(sock, "quit")
	if !playing {
		t.Fatalf("mpv never started playing %s within 75s", roomCode)
	}
	t.Logf("%s is playing (time-pos advanced)", roomCode)
}

func TestE2ETwitchDirectPlayback(t *testing.T) {
	channel, err := topTwitchLiveChannel()
	if err != nil {
		t.Skipf("cannot discover a live twitch channel: %v", err)
	}
	t.Logf("using live twitch channel: %s", channel)
	requirePlayback(t, "tw-"+channel)
}

func TestE2EYoutubeDirectPlayback(t *testing.T) {
	for _, ch := range []string{"bennybennion1", "LofiGirl"} {
		f := streamfinder.NewYoutubeFinder()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := f.Resolve(ctx, "https://www.youtube.com/@"+ch+"/live")
		cancel()
		if err != nil {
			t.Logf("youtube channel %s not resolvable (%v), trying next", ch, err)
			continue
		}
		requirePlayback(t, "yt-"+ch)
		return
	}
	t.Skip("no candidate youtube channel is live right now")
}

// topTwitchLiveChannel asks the public GQL endpoint for a currently live
// channel, mirroring what the finder needs to succeed.
func topTwitchLiveChannel() (string, error) {
	body := strings.NewReader(`{"query":"{ streams(first: 1) { edges { node { broadcaster { login } } } } }"}`)
	req, err := http.NewRequest("POST", "https://gql.twitch.tv/gql", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Client-Id", "kimne78kx3ncx6brgo4mv6wki5h1ko")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Streams struct {
				Edges []struct {
					Node struct {
						Broadcaster struct {
							Login string `json:"login"`
						} `json:"broadcaster"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"streams"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Data.Streams.Edges) == 0 {
		return "", os.ErrNotExist
	}
	return out.Data.Streams.Edges[0].Node.Broadcaster.Login, nil
}
