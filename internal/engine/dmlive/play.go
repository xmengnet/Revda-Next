package dmlive

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"revda/internal/engine/danmaku"
	"revda/internal/engine/ffmpegctl"
	"revda/internal/engine/mpvctl"
	"revda/internal/engine/streamer"
	"revda/internal/engine/streamfinder"
)

type danmakuClient interface {
	Messages() <-chan *danmaku.DanmakuMessage
	Start(context.Context) error
}

// spawnMpv launches mpv with the per-site argument set Rust dmlive uses:
// DASH-direct sites get UA/Referer headers, live sites get caching.
func spawnMpv(ctx context.Context, site Site) (*mpvctl.Mpv, error) {
	if site == SiteBiliVideo {
		return mpvctl.Spawn(ctx, []string{
			"--user-agent=" + streamer.GenUA,
			"--http-header-fields-add=Referer: https://www.bilibili.com/",
		})
	}
	return mpvctl.Spawn(ctx, []string{"--cache=yes", "--cache-pause-initial=yes"})
}

// mpvMonitor closes mpvDone when mpv exits on its own (user closed the
// window), which ends the session.
func (r *Runtime) mpvMonitor(ctx context.Context) {
	r.mpv.Wait(ctx)
	if ctx.Err() == nil {
		select {
		case <-r.mpvDone:
		default:
			close(r.mpvDone)
		}
	}
}

// currentURL is the resolve target; video pages override it when navigating.
func (r *Runtime) currentURL() string {
	if r.pageURL != "" {
		return r.pageURL
	}
	return r.opts.URL
}

// playLive is the live-site play mode: resolve → relay through ffmpeg → mpv
// plays the muxed matroska flow. When the source ends the loop reconnects
// after 1s, until resolve fails or mpv closes.
func (r *Runtime) playLive(ctx context.Context) error {
	mpv, err := spawnMpv(ctx, r.site)
	if err != nil {
		return err
	}
	r.mpv = mpv
	go r.mpvMonitor(ctx)
	go r.handleMpvEvents(ctx)

	playCtx, cancelPlay := context.WithCancel(ctx)
	defer cancelPlay()
	go func() {
		select {
		case <-r.mpvDone:
			cancelPlay()
		case <-playCtx.Done():
		}
	}()

	for {
		if err := r.liveIteration(playCtx, false); err != nil {
			return err
		}
		if playCtx.Err() != nil {
			return playCtx.Err()
		}
		select {
		case <-r.mpvDone:
			return nil
		case <-time.After(time.Second):
		}
	}
}

// recordLive records without mpv, reconnecting across stream restarts.
func (r *Runtime) recordLive(ctx context.Context) error {
	for {
		if err := r.liveIteration(ctx, true); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// liveIteration is one resolve→relay session for live sites (Rust
// play_live). It returns when the source ends.
func (r *Runtime) liveIteration(ctx context.Context, record bool) error {
	info, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	st := detectStreamType(r.site, info.URL)
	logInfof("stream type %d title %q", st, info.Title)

	ffCtx, ffCancel := context.WithCancel(ctx)
	defer ffCancel()

	var dmOnce sync.Once
	startDanmaku := func() {
		dmOnce.Do(func() {
			if client, ok := r.newLiveDanmakuClient(); ok {
				go client.Start(ffCtx) //nolint:errcheck — reconnects internally
				go func() { _ = r.live.Serve(ffCtx, r.danmakuLn, client.Messages()) }()
			}
		})
	}
	// Direct DASH sources have no streamer task, so the danmaku flow starts
	// together with ffmpeg instead of on StreamReady.
	if st == ffmpegctl.TDASH {
		startDanmaku()
	}

	ff, err := ffmpegctl.Run(ffCtx, r.relayOptions(info, st, record))
	if err != nil {
		return err
	}
	ff.OnVideoInfo = func(w, h int) {
		if w > 0 && h > 0 {
			r.settings.RatioScale.Store((16.0 / 9.0) / (float64(w) / float64(h)))
		}
	}
	ff.OnOutputReady = func() {
		if record {
			if r.opts.HTTPAddress == "" {
				cmd, rerr := ffmpegctl.RecordTask(r.f2mURL, info.Title, recordFilename(info.Title, "mkv"))
				if rerr != nil {
					logErrorf("record task: %v", rerr)
					return
				}
				go func() { _ = cmd.Wait() }() // finalizes on its own after the relay dies
			}
			return
		}
		if r.mpv != nil {
			_ = r.mpv.LoadFile(r.f2mURL, "")
			_ = r.mpv.Command("set_property", "sub-visibility", true)
		}
	}

	ffDone := make(chan struct{})
	go func() { _ = ff.Wait(); close(ffDone) }()

	streamErr := r.runStreamer(ffCtx, info, st, startDanmaku)
	if streamErr != nil {
		logInfof("streamer exit: %v", streamErr)
	}
	_ = ff.Quit() // flush the container, like Rust's fc.quit()
	select {
	case <-ffDone:
	case <-time.After(5 * time.Second):
		ffCancel()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// relayOptions builds the ffmpeg relay command inputs for one session.
func (r *Runtime) relayOptions(info *streamfinder.StreamInfo, st ffmpegctl.StreamType, record bool) ffmpegctl.Options {
	opts := ffmpegctl.Options{
		Mode:       modeOf(record),
		StreamType: st,
		Title:      info.Title,
		VideoURL:   r.videoURL,
		DanmakuURL: r.danmakuURL,
		Output:     r.f2mURL,
	}
	if record && r.opts.HTTPAddress != "" {
		opts.Output = r.opts.HTTPAddress
	}
	if st == ffmpegctl.TDASH {
		opts.DashInputs = []ffmpegctl.DashInput{dashInput(info, info.URL)}
		if info.AudioURL != "" {
			opts.DashInputs = append(opts.DashInputs, dashInput(info, info.AudioURL))
		}
	}
	return opts
}

func dashInput(info *streamfinder.StreamInfo, u string) ffmpegctl.DashInput {
	in := ffmpegctl.DashInput{URL: u}
	for k, v := range info.Headers {
		if strings.EqualFold(k, "User-Agent") {
			in.UA = v
		}
		if strings.EqualFold(k, "Referer") {
			in.Referer = v
		}
	}
	return in
}

func modeOf(record bool) ffmpegctl.RunMode {
	if record {
		return ffmpegctl.ModeRecord
	}
	return ffmpegctl.ModePlay
}

// runStreamer dispatches the downloader for the resolved stream type. DASH
// sources are read by ffmpeg directly and need no streamer.
func (r *Runtime) runStreamer(ctx context.Context, info *streamfinder.StreamInfo, st ffmpegctl.StreamType, onReady func()) error {
	switch st {
	case ffmpegctl.TFLV:
		cookie := ""
		if r.opts.Plive && r.site == SiteBiliLive {
			cookie = r.bcookie
		}
		return streamer.RunFLV(ctx, info.URL, r.opts.URL, cookie, r.opts.Plive, r.videoLn, onReady)
	case ffmpegctl.THLS0, ffmpegctl.THLS1:
		h := streamer.NewHLS(info.URL, "", r.site == SiteTwitchLive)
		return h.Run(ctx, r.videoLn, onReady)
	default: // TDASH
		return nil
	}
}

// playVideo plays bilibili videos/bangumi: mpv takes the DASH EDL directly
// and pages advance on end-file / keybinds.
func (r *Runtime) playVideo(ctx context.Context) error {
	mpv, err := spawnMpv(ctx, r.site)
	if err != nil {
		return err
	}
	r.mpv = mpv
	go r.mpvMonitor(ctx)
	go r.handleMpvEvents(ctx)

	r.loadVideoPage(ctx, r.page)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.mpvDone:
		return nil
	}
}

// loadVideoPage resolves one page and points mpv at it. Serialized: page
// keybinds can fire faster than a resolve finishes.
func (r *Runtime) loadVideoPage(ctx context.Context, page int) {
	if r.mpv == nil {
		return
	}
	r.pageMu.Lock()
	defer r.pageMu.Unlock()
	if ctx.Err() != nil {
		return
	}

	r.page = page
	r.pageURL = withPage(r.opts.URL, page)
	info, err := r.resolve(ctx)
	if err != nil {
		logErrorf("resolve video page %d: %v", page, err)
		return
	}
	r.lastDanmakuID = info.DanmakuID

	edl := fmt.Sprintf("edl://!no_clip;!no_chapters;%%%d%%%s;!new_stream;!no_clip;!no_chapters;%%%d%%%s",
		len(info.AudioURL), info.AudioURL, len(info.URL), info.URL)
	logInfof("load video page %d: %s", page, info.Title)
	_ = r.mpv.LoadFile(edl, info.Title)
}

// serveVideoDanmaku fetches a video's complete danmaku and streams the ASS
// document on the danmaku socket (to mpv's sub-add or the recorder's ffmpeg).
func (r *Runtime) serveVideoDanmaku(ctx context.Context, cid string, ratio float64) {
	r.settings.RatioScale.Store(ratio)
	fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	msgs, err := danmaku.FetchVideoDanmaku(fctx, nil, cid)
	if err != nil {
		logErrorf("fetch video danmaku: %v", err)
		return
	}
	_ = danmaku.ServeStaticASS(ctx, r.danmakuLn, msgs, r.settings)
}

// recordBiliVideo records a bilibili video to file (or serves it over
// --http-address) without mpv. Danmaku rides the relay like live mode.
func (r *Runtime) recordBiliVideo(ctx context.Context) error {
	info, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	r.lastDanmakuID = info.DanmakuID

	ffCtx, ffCancel := context.WithCancel(ctx)
	defer ffCancel()

	ff, err := ffmpegctl.Run(ffCtx, r.relayOptions(info, ffmpegctl.TDASH, true))
	if err != nil {
		return err
	}
	ff.OnVideoInfo = func(w, h int) {
		if w > 0 && h > 0 {
			ratio := 16.0 * float64(h) / float64(w) / 9.0
			go r.serveVideoDanmaku(ffCtx, r.lastDanmakuID, ratio)
		}
	}
	ff.OnOutputReady = func() {
		if r.opts.HTTPAddress == "" {
			cmd, rerr := ffmpegctl.RecordTask(r.f2mURL, info.Title, recordFilename(info.Title, "mkv"))
			if rerr != nil {
				logErrorf("record task: %v", rerr)
				return
			}
			go func() { _ = cmd.Wait() }()
		}
	}
	_ = ff.Wait()
	return nil // recording finished
}

// downloadDanmaku stores only the danmaku as an .ass file (--download-dm).
func (r *Runtime) downloadDanmaku(ctx context.Context) error {
	r.settings.RatioScale.Store(1.0)
	info, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	var msgs []danmaku.VideoDanmaku
	if r.site == SiteBahaVideo {
		msgs, err = danmaku.FetchBahaDanmaku(ctx, nil, info.DanmakuID)
	} else {
		fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		msgs, err = danmaku.FetchVideoDanmaku(fctx, nil, info.DanmakuID)
	}
	if err != nil {
		return err
	}

	filename := recordFilename(info.Title, "ass")
	cmd, err := ffmpegctl.DanmakuOnlyTask(r.danmakuURL, filename)
	if err != nil {
		return err
	}
	// ffmpeg dials the danmaku socket at startup; serving writes the whole
	// document and closes, which flushes the file and ends ffmpeg.
	if err := danmaku.ServeStaticASS(ctx, r.danmakuLn, msgs, r.settings); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	_ = cmd.Wait()
	logInfof("danmaku written to %s", filename)
	return nil
}

// detectStreamType maps the resolved URL to the relay input shape (Rust
// ConfigManager::set_stream_type).
func detectStreamType(site Site, rawURL string) ffmpegctl.StreamType {
	if site == SiteBiliVideo {
		return ffmpegctl.TDASH
	}
	if strings.Contains(rawURL, ".m3u8") {
		if site == SiteBiliLive {
			return ffmpegctl.THLS1 // m4s inside
		}
		return ffmpegctl.THLS0 // ts inside
	}
	if strings.Contains(rawURL, ".flv") {
		return ffmpegctl.TFLV
	}
	return ffmpegctl.TDASH
}

// newLiveDanmakuClient picks the protocol client for the current site.
func (r *Runtime) newLiveDanmakuClient() (danmakuClient, bool) {
	roomID := lastPathSeg(r.opts.URL)
	switch r.site {
	case SiteBiliLive:
		return danmaku.NewBilibiliDanmakuClient(roomID), true
	case SiteDouyuLive:
		return danmaku.NewDouyuDanmakuClient(roomID), true
	case SiteHuyaLive:
		return danmaku.NewHuyaDanmakuClient(roomID), true
	case SiteTwitchLive:
		return danmaku.NewTwitchDanmakuClient(roomID), true
	case SiteYoutubeLive:
		return danmaku.NewYoutubeDanmakuClient(r.currentURL()), true
	default:
		return nil, false
	}
}

// lastPathSeg extracts the room/video identifier from a URL path.
func lastPathSeg(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			return segs[i]
		}
	}
	return raw
}

// withPage rewrites the ?p= query of a URL.
func withPage(rawURL string, page int) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if page > 0 {
		q.Set("p", strconv.Itoa(page))
	} else {
		q.Del("p")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
