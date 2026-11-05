package player

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"revda/internal/config"
)

// TestRecordHasDanmakuE2E records a short live segment and requires the
// finished file to carry an ASS subtitle track. This is the regression test
// for "recording has no danmaku". Override the room (which must have chat
// traffic) with REVDA_RECORD_ROOM.
func TestRecordHasDanmakuE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	roomCode := os.Getenv("REVDA_RECORD_ROOM")
	if roomCode == "" {
		roomCode = "do-1126960"
	}
	cfg := config.DmliveConfig{DanmakuSpeed: 8000, FontAlpha: 0, FontScale: 1.0, RecordPath: t.TempDir()}
	ipcSock, _, outPath, err := launchDirect(roomCode, cfg, true, []string{"--vo=null", "--ao=null"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	// Let danmaku accumulate, then stop mpv; recordDanmaku muxes on ctx.Done.
	time.Sleep(30 * time.Second)
	_ = mpvCommand(ipcSock, "quit")

	// Poll the file itself for the subtitle track: the mux is asynchronous, so
	// the recording exists well before the track is added.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	deadline := time.Now().Add(120 * time.Second)
	for {
		out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
			"-show_entries", "stream=codec_type,codec_name",
			"-of", "default=noprint_wrappers=1", outPath).CombinedOutput()
		if err == nil && strings.Contains(string(out), "codec_type=subtitle") {
			t.Logf("recording has danmaku track: %s\n%s", outPath, out)
			if !strings.Contains(string(out), "codec_name=ass") {
				t.Fatalf("subtitle track is not ASS (danmaku animation lost):\n%s", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no subtitle track after 120s: %v\n%s", err, out)
		}
		time.Sleep(2 * time.Second)
	}
}
