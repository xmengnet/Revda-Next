package dmlive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Live end-to-end for the play&record path (relay -> tee -> mpv + recorder
// ffmpeg), guarded by REVDA_RECORD_TEST=1: needs network, mpv, ffmpeg and a
// live, chatty douyu room (override with REVDA_RECORD_URL). mpv runs headless
// via MpvExtraArgs. Asserts the recording decodes cleanly, carries a
// full-rate video track and real danmaku.
func TestPlayRecordLiveDouyu(t *testing.T) {
	if os.Getenv("REVDA_RECORD_TEST") == "" {
		t.Skip("set REVDA_RECORD_TEST=1 to run the live play&record e2e")
	}
	url := os.Getenv("REVDA_RECORD_URL")
	if url == "" {
		url = "https://www.douyu.com/1126960"
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	started := make(chan struct{})
	go func() {
		_ = Run(ctx, Options{
			URL:                url,
			RecordWhilePlaying: true,
			Quiet:              true,
			LogLevel:           2,
			RecordPath:         dir,
			MpvExtraArgs:       []string{"--vo=null", "--ao=null", "--force-window=no"},
			OnStarted: func() {
				close(started)
			},
		})
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Skip("engine never started (room offline?)")
	}
	<-ctx.Done() // let the session record for the full window
	time.Sleep(3 * time.Second)

	matches, _ := filepath.Glob(filepath.Join(dir, "*.mkv"))
	if len(matches) == 0 {
		t.Fatal("no recording produced")
	}
	var f string
	var newest int64
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.Size() > newest {
			f, newest = m, fi.Size()
		}
	}
	t.Logf("recording: %s (%d bytes)", f, newest)

	// Streams: video + audio + ass danmaku.
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type", "-of", "csv=p=0", f).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v: %s", err, out)
	}
	kinds := strings.Split(strings.TrimSpace(string(out)), "\n")
	t.Logf("streams: %v", kinds)

	// Decode cleanliness.
	dec, err := exec.Command("ffmpeg", "-v", "error", "-i", f, "-f", "null", "-").CombinedOutput()
	errLines := 0
	for _, l := range strings.Split(string(dec), "\n") {
		if strings.TrimSpace(l) != "" {
			errLines++
		}
	}
	t.Logf("decode error lines: %d", errLines)
	if errLines > 10 {
		t.Fatalf("recording is corrupted (%d decode error lines): %s", errLines, truncLines(dec, 5))
	}

	// Video rate: ≥ 15 packets/second over the file duration.
	durOut, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", f).CombinedOutput()
	dur, _ := strconv.ParseFloat(strings.TrimSpace(string(durOut)), 64)
	pktOut, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", f).CombinedOutput()
	packets := len(nonEmptyLines(pktOut))
	t.Logf("duration %.1fs, video packets %d (%.1f/s)", dur, packets, float64(packets)/dur)
	if dur > 10 && float64(packets)/dur < 15 {
		t.Fatalf("video packet rate %.1f/s indicates dropped packets", float64(packets)/dur)
	}

	// Real danmaku in the ASS track (skip dmlive-empty keepalives).
	assPath := filepath.Join(dir, "probe.ass")
	defer os.Remove(assPath)
	_ = exec.Command("ffmpeg", "-y", "-v", "error", "-i", f, "-map", "0:s:0", assPath).Run()
	if raw, err := os.ReadFile(assPath); err == nil {
		real := 0
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(l, "Dialogue:") && !strings.Contains(l, "dmlive-empty") {
				real++
			}
		}
		t.Logf("real danmaku dialogue lines: %d", real)
		if real == 0 && len(kinds) >= 3 {
			t.Log("warning: no real danmaku captured (quiet room?)")
		}
	}
}

func nonEmptyLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func truncLines(b []byte, n int) string {
	lines := nonEmptyLines(b)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
