package player

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"revda/internal/config"
	"revda/internal/engine/danmaku"
	"revda/internal/engine/streamfinder"
)

// LaunchDirect resolves the stream URL using the pure Go engine and launches
// mpv directly, with live danmaku served as an ASS snapshot over local HTTP.
func LaunchDirect(roomCode string) error {
	return LaunchDirectWithConfig(roomCode, config.DmliveConfig{
		DanmakuSpeed: 8000,
		FontAlpha:    0,
		FontScale:    1.0,
	})
}

// LaunchDirectWithConfig is LaunchDirect with explicit danmaku settings. It
// returns as soon as mpv has been spawned; subtitle mounting and the refresh
// loop continue in background goroutines and are torn down when mpv exits.
func LaunchDirectWithConfig(roomCode string, cfg config.DmliveConfig) error {
	return LaunchDirectWithConfigRecord(roomCode, cfg, false)
}

// LaunchDirectWithConfigRecord additionally remuxes the stream (plus the
// danmaku subtitle track) into a local file under the user's video directory.
func LaunchDirectWithConfigRecord(roomCode string, cfg config.DmliveConfig, record bool) error {
	_, _, _, err := launchDirect(roomCode, cfg, record, nil)
	return err
}

// launchDirect is the shared body; extraArgs lets tests force mpv headless.
// When record is true the stream is remuxed to a local file. It returns the
// path of the mpv IPC socket, the danmaku sub URL ("" when the room has no
// danmaku client), and the recording output path ("" when not recording).
func launchDirect(roomCode string, cfg config.DmliveConfig, record bool, extraArgs []string) (string, string, string, error) {
	ctx, cancel := context.WithCancel(context.Background())

	var finder streamfinder.Finder
	var targetURL string
	var dmRoomID string
	var recordPath string

	switch {
	case strings.HasPrefix(roomCode, "bi-"):
		finder = streamfinder.NewBilibiliFinder(cfg.BCookie)
		targetURL = "https://live.bilibili.com/" + strings.TrimPrefix(roomCode, "bi-")
		dmRoomID = strings.TrimPrefix(roomCode, "bi-")
	case strings.HasPrefix(roomCode, "do-"):
		finder = streamfinder.NewDouyuFinder()
		targetURL = "https://www.douyu.com/" + strings.TrimPrefix(roomCode, "do-")
		dmRoomID = strings.TrimPrefix(roomCode, "do-")
	case strings.HasPrefix(roomCode, "hu-"):
		finder = streamfinder.NewHuyaFinder()
		targetURL = "https://www.huya.com/" + strings.TrimPrefix(roomCode, "hu-")
		dmRoomID = strings.TrimPrefix(roomCode, "hu-")
	case strings.HasPrefix(roomCode, "yt-"):
		// YouTube/Twitch playback has no danmaku client in the Go engine yet.
		finder = streamfinder.NewYoutubeFinder()
		targetURL = "https://www.youtube.com/@" + strings.TrimPrefix(roomCode, "yt-") + "/live"
	case strings.HasPrefix(roomCode, "ytv-"):
		finder = streamfinder.NewYoutubeFinder()
		targetURL = "https://www.youtube.com/watch?v=" + strings.TrimPrefix(roomCode, "ytv-")
	case strings.HasPrefix(roomCode, "tw-"):
		finder = streamfinder.NewTwitchFinder()
		targetURL = "https://www.twitch.tv/" + strings.TrimPrefix(roomCode, "tw-")
	case strings.HasPrefix(roomCode, "BV"), strings.HasPrefix(roomCode, "av"):
		// Regular bilibili videos: DASH via WBI-signed playurl, danmaku from
		// the static per-video XML (cid comes back in StreamInfo.DanmakuID).
		finder = streamfinder.NewBiliVideoFinder(cfg.BCookie)
		targetURL = "https://www.bilibili.com/video/" + roomCode
		if i := strings.Index(roomCode, ":"); i >= 0 { // "BVxxx:2" multi-page form
			targetURL = fmt.Sprintf("https://www.bilibili.com/video/%s?p=%s", roomCode[:i], roomCode[i+1:])
		}
	case strings.HasPrefix(roomCode, "ep"), strings.HasPrefix(roomCode, "ss"):
		// Bangumi: the season's episode list resolves to a bvid/cid pair,
		// which then follows the ordinary video playurl path. "ep123" is the
		// episode itself; "ss123:3" is season ss123, episode 3.
		finder = streamfinder.NewBiliVideoFinder(cfg.BCookie)
		seg := roomCode
		if i := strings.Index(roomCode, ":"); i >= 0 {
			seg = roomCode[:i]
			targetURL = fmt.Sprintf("https://www.bilibili.com/bangumi/play/%s?p=%s", seg, roomCode[i+1:])
		} else {
			targetURL = "https://www.bilibili.com/bangumi/play/" + seg
		}
	default:
		cancel()
		return "", "", "", wrapPlayError(fmt.Errorf("direct Go stream resolution not supported for %s", roomCode), platformOf(roomCode), roomCode)
	}

	resolveCtx, resolveCancel := context.WithTimeout(ctx, 20*time.Second)
	info, err := finder.Resolve(resolveCtx, targetURL)
	resolveCancel()
	if err != nil {
		cancel()
		return "", "", "", wrapPlayError(fmt.Errorf("resolve stream error: %w", err), platformOf(roomCode), roomCode)
	}

	// The danmaku HTTP endpoint must be up before mpv tries to mount it.
	// Deliberately no wait for the first message here: mpv starts right away
	// and mountDanmaku below handles the timing.
	var subURL string
	var renderer *danmaku.Renderer
	var staticSub bool
	switch {
	case dmRoomID != "":
		if u, r, ok := startDanmaku(ctx, roomCode, dmRoomID, cfg); ok {
			subURL, renderer = u, r
		}
	case strings.HasPrefix(roomCode, "BV"), strings.HasPrefix(roomCode, "av"),
		strings.HasPrefix(roomCode, "ep"), strings.HasPrefix(roomCode, "ss"):
		if cid := info.DanmakuID; cid != "" {
			if u, ok := startVideoDanmaku(ctx, cid, cfg); ok {
				subURL, staticSub = u, true
			}
		}
	}

	args := []string{
		composePlayURL(info),
		"--cache=yes",
		"--cache-pause-initial=yes",
		fmt.Sprintf("--force-media-title=%s", info.Title),
	}
	for k, v := range info.Headers {
		args = append(args, fmt.Sprintf("--http-header-fields-add=%s: %s", k, v))
	}
	if subURL != "" {
		args = append(args, "--sub-visibility=yes")
	}
	if record {
		outPath, err := recordOutputPath(roomCode, info.Title, cfg.RecordPath)
		if err != nil {
			cancel()
			return "", "", "", wrapPlayError(fmt.Errorf("prepare recording path: %w", err), platformOf(roomCode), roomCode)
		}
		// --stream-record remuxes the source streams into a container without
		// re-encoding; the danmaku ASS track is muxed in below via IPC once
		// mounted, so the recording carries a burned-in subtitle track.
		args = append(args, "--stream-record="+outPath)
		recordPath = outPath
	}
	args = append(args, extraArgs...)

	ipcSock := fmt.Sprintf("/tmp/revda-mpv-%d.sock", time.Now().UnixNano()%1e9)
	args = append(args, fmt.Sprintf("--input-ipc-server=%s", ipcSock))

	cmd := exec.Command("mpv", args...)
	if err := cmd.Start(); err != nil {
		cancel()
		return "", "", "", wrapPlayError(fmt.Errorf("failed to launch mpv: %w", err), platformOf(roomCode), roomCode)
	}
	// The context lives exactly as long as mpv. A deferred cancel here fired
	// on return and killed the danmaku pipeline within a second — the reason
	// the window never showed any danmaku.
	go func() {
		_ = cmd.Wait()
		cancel()
	}()

	switch {
	case staticSub:
		go mountStaticSub(ctx, ipcSock, subURL)
	case renderer != nil:
		// Anchor the ASS timeline at mpv spawn; mountDanmaku calibrates it
		// against mpv's actual time-pos.
		renderer.SetClock(time.Now())
		go mountDanmaku(ctx, ipcSock, subURL, renderer)
	}
	if recordPath != "" {
		go recordDanmaku(ctx, ipcSock, subURL, renderer, recordPath)
	}
	return ipcSock, subURL, recordPath, nil
}

func startDanmaku(ctx context.Context, roomCode, roomID string, cfg config.DmliveConfig) (string, *danmaku.Renderer, bool) {
	var client interface {
		Messages() <-chan *danmaku.DanmakuMessage
		Start(context.Context) error
	}

	switch {
	case strings.HasPrefix(roomCode, "bi-"):
		client = danmaku.NewBilibiliDanmakuClient(roomID)
	case strings.HasPrefix(roomCode, "do-"):
		client = danmaku.NewDouyuDanmakuClient(roomID)
	default: // hu-
		client = danmaku.NewHuyaDanmakuClient(roomID)
	}

	renderer := danmaku.NewRenderer(cfg.FontScale, cfg.FontAlpha, int(cfg.DanmakuSpeed))
	subURL, err := danmaku.StartHTTP(ctx, client.Messages(), renderer)
	if err != nil {
		return "", nil, false
	}

	go func() { _ = client.Start(ctx) }()
	return subURL, renderer, true
}

// mountDanmaku mounts the ASS snapshot as mpv's subtitle track once a media
// file is loaded, then keeps it in sync: sub-reload makes mpv re-read the
// snapshot, and time-pos polls recalibrate the ASS timeline to mpv's playback
// clock (startup delay and buffering would otherwise desync them).
func mountDanmaku(ctx context.Context, ipcSock, subURL string, r *danmaku.Renderer) {
	if !waitForSocket(ctx, ipcSock, 15*time.Second) {
		return
	}
	waitMediaLoaded(ctx, ipcSock)
	// Wait for the first message so the mounted track has content; a quiet
	// room still mounts after the timeout and self-heals on a later reload.
	for i := 0; i < 40; i++ {
		if ctx.Err() != nil {
			return
		}
		if r.HasContent() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !subAddSelect(ctx, ipcSock, subURL) {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := mpvCommand(ipcSock, "sub-reload"); err != nil {
				return // mpv is gone
			}
			if pos, err := mpvGetProperty(ipcSock, "time-pos"); err == nil && pos >= 0 {
				r.SetClock(time.Now().Add(-time.Duration(pos * float64(time.Second))))
			}
		}
	}
}

// mountStaticSub mounts a fixed ASS document (bilibili video danmaku). Its
// timestamps are video positions, so a single sub-add suffices — no reload
// loop, no clock calibration.
func mountStaticSub(ctx context.Context, ipcSock, subURL string) {
	if !waitForSocket(ctx, ipcSock, 15*time.Second) {
		return
	}
	waitMediaLoaded(ctx, ipcSock)
	subAddSelect(ctx, ipcSock, subURL)
}

// waitMediaLoaded blocks until mpv has a file open (sub-add fails before
// that) or gives up after ~30s.
func waitMediaLoaded(ctx context.Context, ipcSock string) {
	for i := 0; i < 60; i++ {
		if ctx.Err() != nil {
			return
		}
		if _, err := mpvGetString(ipcSock, "filename"); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// subAddSelect retries the sub-add until it sticks; returns false when the
// context ended first.
func subAddSelect(ctx context.Context, ipcSock, subURL string) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if err := mpvCommand(ipcSock, "sub-add", subURL, "select"); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// startVideoDanmaku downloads a bilibili video's complete danmaku and serves
// it as one static ASS snapshot.
func startVideoDanmaku(ctx context.Context, cid string, cfg config.DmliveConfig) (string, bool) {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msgs, err := danmaku.FetchVideoDanmaku(fetchCtx, nil, cid)
	if err != nil {
		return "", false
	}
	ass := danmaku.RenderVideoASS(msgs, cfg.FontScale, cfg.FontAlpha, int(cfg.DanmakuSpeed))
	subURL, err := danmaku.StartStaticHTTP(ctx, ass)
	if err != nil {
		return "", false
	}
	return subURL, true
}

// recordOutputPath builds the destination for a recording. The directory is
// cfg.RecordPath when set, otherwise ~/Videos/revda; it is created on demand.
func recordOutputPath(roomCode, title, recordDir string) (string, error) {
	dir := recordDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "Videos", "revda")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	safe := sanitizeFilename(title)
	if safe == "" {
		safe = sanitizeFilename(roomCode)
	}
	// .mkv, not .mp4: the danmaku track is muxed back in as ASS, which mp4
	// cannot carry (its mov_text codec drops the scroll animation).
	name := fmt.Sprintf("%s-%s.mkv", time.Now().Format("20060102-150405"), safe)
	return filepath.Join(dir, name), nil
}

// sanitizeFilename strips path separators and characters that are illegal or
// awkward on common filesystems, and caps the length.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' ||
			r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		case r < 0x20:
			// control character
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 80 {
		out = strings.TrimSpace(out[:80])
	}
	return out
}

func waitForSocket(ctx context.Context, sock string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if conn, err := net.DialTimeout("unix", sock, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

var ipcRequestID atomic.Uint64

// mpvRequest sends one command over mpv's IPC socket and returns the reply's
// data field. mpv interleaves async events with replies on the socket, so
// lines are matched by request_id.
func mpvRequest(sock string, command []any) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	reqID := ipcRequestID.Add(1)
	req, _ := json.Marshal(map[string]any{"command": command, "request_id": reqID})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, err
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var reply struct {
			Error     string          `json:"error"`
			RequestID uint64          `json:"request_id"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &reply); err != nil {
			continue
		}
		if reply.RequestID != reqID {
			continue // an event or another client's reply
		}
		if reply.Error != "success" {
			return nil, fmt.Errorf("mpv %v: %s", command, reply.Error)
		}
		return reply.Data, nil
	}
	return nil, fmt.Errorf("no reply from mpv for %v", command)
}

func mpvCommand(sock string, command ...any) error {
	_, err := mpvRequest(sock, command)
	return err
}

func mpvGetProperty(sock, prop string) (float64, error) {
	data, err := mpvRequest(sock, []any{"get_property", prop})
	if err != nil {
		return 0, err
	}
	var v float64
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, fmt.Errorf("mpv property %s not numeric", prop)
	}
	return v, nil
}

func mpvGetString(sock, prop string) (string, error) {
	data, err := mpvRequest(sock, []any{"get_property", prop})
	if err != nil {
		return "", err
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("mpv property %s not a string", prop)
	}
	return v, nil
}

// recordDanmaku muxes the danmaku ASS into the recording once mpv has
// finished it. --stream-record writes the source streams only; this runs
// ffmpeg afterwards to add the subtitle track, producing the same
// "danmaku inside the recorded file" experience the Rust dmlive offered.
// It is best-effort: the raw recording stays valid if muxing fails.
func recordDanmaku(ctx context.Context, ipcSock, subURL string, r *danmaku.Renderer, outPath string) {
	// Live rooms stream their danmaku into the renderer; videos get a static
	// ASS document from the HTTP endpoint, which we can snapshot at any time.
	var staticAss string
	if r == nil {
		if subURL == "" {
			return
		}
		resp, err := http.Get(subURL)
		if err != nil {
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return
		}
		staticAss = string(body)
	}

	// Wait for mpv to exit: the renderer accumulates the whole session, so the
	// final snapshot must be taken after the last message, not partway through.
	<-ctx.Done()

	var ass string
	if r != nil {
		ass = r.WriteASS()
	} else {
		ass = staticAss
	}
	if !strings.Contains(ass, "Dialogue:") {
		return // no danmaku at all; leave the raw recording untouched
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

	// mkv rather than mp4: mp4 has no ASS codec (mov_text drops the \move
	// tags, which is what makes danmaku scroll), while matroska carries the
	// subtitle stream as-is.
	tmp := outPath + ".mux.mkv"
	cmd := exec.Command("ffmpeg", "-y", "-i", outPath, "-i", assPath,
		"-map", "0", "-map", "1", "-c", "copy", "-c:s", "ass", tmp)
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, outPath)
}

// composePlayURL merges separate video/audio streams (YouTube live) into an
// mpv EDL playlist; the %n% prefix tells mpv the byte length of each URL so
// embedded special characters stay verbatim. Same shape dmlive uses for
// bilibili DASH.
func composePlayURL(info *streamfinder.StreamInfo) string {
	if info.AudioURL == "" {
		return info.URL
	}
	return fmt.Sprintf("edl://!no_clip;!no_chapters;%%%d%%%s;!new_stream;!no_clip;!no_chapters;%%%d%%%s",
		len(info.AudioURL), info.AudioURL, len(info.URL), info.URL)
}

// CanResolveDirectly checks if Go engine can resolve this room
func CanResolveDirectly(roomCode string) bool {
	return strings.HasPrefix(roomCode, "bi-") ||
		strings.HasPrefix(roomCode, "do-") ||
		strings.HasPrefix(roomCode, "hu-") ||
		strings.HasPrefix(roomCode, "yt-") ||
		strings.HasPrefix(roomCode, "ytv-") ||
		strings.HasPrefix(roomCode, "tw-") ||
		strings.HasPrefix(roomCode, "BV") ||
		strings.HasPrefix(roomCode, "av") ||
		strings.HasPrefix(roomCode, "ep") ||
		strings.HasPrefix(roomCode, "ss")
}
