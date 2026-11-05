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

// Shapes mirror a real `yt-dlp -J` dump for a YouTube live HLS stream: the
// audio variants 233/234 carry vcodec "none", acodec null and resolution
// "audio only".
func TestSelectYtDlpStreams(t *testing.T) {
	audioLow := ytDlpFormat{URL: "https://audio/low", VCodec: "none", Resolution: "audio only"}
	audioHigh := ytDlpFormat{URL: "https://audio/high", VCodec: "none", Resolution: "audio only"}
	video := ytDlpFormat{URL: "https://video/1080", VCodec: "avc1.640028", ACodec: "none", TBR: 4561, Height: 1080}
	videoNoBitrate := ytDlpFormat{URL: "https://video/nobr", VCodec: "avc1.4D401F", ACodec: "none"}
	combined := ytDlpFormat{URL: "https://combined", VCodec: "avc1", ACodec: "mp4a", TBR: 1000}

	tests := []struct {
		name     string
		formats  []ytDlpFormat
		wantURL  string
		wantAURL string
	}{
		{"hls live split av", []ytDlpFormat{audioLow, video, audioHigh}, video.URL, audioHigh.URL},
		{"combined wins", []ytDlpFormat{combined, video, audioHigh}, combined.URL, ""},
		{"video only", []ytDlpFormat{{URL: ""}, video}, video.URL, ""},
		{"null video bitrate still selected", []ytDlpFormat{videoNoBitrate}, videoNoBitrate.URL, ""},
		{"unclassifiable formats skipped", []ytDlpFormat{{URL: "https://x", VCodec: "none"}}, "", ""},
		{"all urls empty", []ytDlpFormat{{VCodec: "avc1"}}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotAURL := selectYtDlpStreams(tt.formats)
			if gotURL != tt.wantURL || gotAURL != tt.wantAURL {
				t.Fatalf("selectYtDlpStreams() = (%q, %q), want (%q, %q)",
					gotURL, gotAURL, tt.wantURL, tt.wantAURL)
			}
		})
	}
}
