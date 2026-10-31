package dmlive

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"revda/internal/engine/danmaku"
)

// muxVideoDanmaku finishes a video "play & record" session: the stream-record
// file holds the bare DASH streams (the static danmaku track lives outside
// the EDL), so the kept ASS document is muxed in once mpv has exited — the
// same post-step the direct player used. Best effort: the raw recording
// stays valid if muxing fails.
func (r *Runtime) muxVideoDanmaku() {
	r.recordMu.Lock()
	outPath := r.recordPath
	r.recordMu.Unlock()
	if outPath == "" {
		return
	}
	r.lastVideoMu.Lock()
	msgs := r.lastVideoMsgs
	r.lastVideoMu.Unlock()
	if len(msgs) == 0 {
		return
	}
	ass := danmaku.RenderVideoASS(msgs, r.settings.FontScale.Load(), r.settings.FontAlpha.Load(), int(r.settings.SpeedMS.Load()))
	if !strings.Contains(ass, "Dialogue:") {
		return
	}

	assPath := outPath + ".danmaku.ass"
	if err := os.WriteFile(assPath, []byte(ass), 0644); err != nil {
		return
	}
	defer os.Remove(assPath)

	// mpv may still be flushing the container for a moment after it exits.
	for i := 0; i < 20; i++ {
		if fi, err := os.Stat(outPath); err == nil && fi.Size() > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// matroska carries the ASS subtitle stream as-is; mp4's mov_text would
	// drop the \move tags that make danmaku scroll.
	tmp := outPath + ".mux.mkv"
	cmd := exec.Command("ffmpeg", "-y", "-i", outPath, "-i", assPath,
		"-map", "0", "-map", "1", "-c", "copy", "-c:s", "ass", tmp)
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		logWarnf("danmaku mux failed, keeping raw recording: %v", err)
		return
	}
	_ = os.Rename(tmp, outPath)
	logInfof("danmaku muxed into %s", outPath)
}
