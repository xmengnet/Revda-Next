// Package dmlive is the Go port of the Rust dmlive engine: a standalone CLI
// that resolves a room URL, relays the stream through ffmpeg (muxing the
// live danmaku as ASS subtitle tracks) and drives mpv or a recorder.
package dmlive

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"revda/internal/browsercookies"
	"revda/internal/config"
	"revda/internal/engine/danmaku"
	"revda/internal/engine/ffmpegctl"
	"revda/internal/engine/mpvctl"
	"revda/internal/engine/streamfinder"
)

// Options mirrors Rust dmlive's clap Args + ConfigManager.
type Options struct {
	URL          string
	Record       bool
	DownloadDM   bool
	WaitInterval uint64
	Quiet        bool
	Plive        bool
	// HTTPAddress makes record mode serve the matroska output over HTTP
	// instead of the local f2m socket (Rust --http-address).
	HTTPAddress string
	// LogLevel: 1 debug, 2 info, 3 warn (default), 4 error.
	LogLevel int
	// RecordWhilePlaying is the GUI's "play & record": keep the mpv window
	// and additionally record the session. Live danmaku rides the relayed
	// matroska; video danmaku is muxed in after mpv exits.
	RecordWhilePlaying bool
	// ResolveAttempts caps the resolve retry rounds (0 = Rust default 20,
	// 3s apart).
	ResolveAttempts int
	// OnStarted fires once when mpv has been spawned in a play mode, giving
	// GUI callers an "it's up" signal long before Run returns.
	OnStarted func()
	// MpvExtraArgs appends arguments to every mpv spawn (headless test
	// runs, site-specific workarounds).
	MpvExtraArgs []string
	// RecordPath overrides the recording directory (config record_path or
	// ~/Videos/revda when empty).
	RecordPath string
}

type Site int

const (
	SiteBiliLive Site = iota
	SiteBiliVideo
	SiteBahaVideo
	SiteDouyinLive
	SiteDouyuLive
	SiteHuyaLive
	SiteTwitchLive
	SiteYoutubeLive
)

// DetectSite classifies a room URL and extracts the ?p= page number used by
// bilibili videos / bangumi and baha seasons.
func DetectSite(rawURL string) (Site, int, error) {
	rawURL = strings.ReplaceAll(rawURL, "dmlive://", "https://")
	switch {
	case strings.Contains(rawURL, "live.bilibili.com/"):
		return SiteBiliLive, pageOf(rawURL), nil
	case strings.Contains(rawURL, "bilibili.com/"):
		return SiteBiliVideo, pageOf(rawURL), nil
	case strings.Contains(rawURL, "ani.gamer.com.tw/"):
		return SiteBahaVideo, pageOf(rawURL), nil
	case strings.Contains(rawURL, "douyin.com/"):
		return SiteDouyinLive, 0, nil
	case strings.Contains(rawURL, "douyu.com/"):
		return SiteDouyuLive, 0, nil
	case strings.Contains(rawURL, "huya.com/"):
		return SiteHuyaLive, 0, nil
	case strings.Contains(rawURL, "twitch.tv/"):
		return SiteTwitchLive, 0, nil
	case strings.Contains(rawURL, "youtube.com/"):
		return SiteYoutubeLive, 0, nil
	default:
		return 0, 0, fmt.Errorf("unknown url: %s", rawURL)
	}
}

func pageOf(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	p := u.Query().Get("p")
	if p == "" {
		return 0
	}
	n := 0
	fmt.Sscanf(p, "%d", &n)
	return n
}

// Runtime is one dmlive session: sockets, config state and the mpv handle.
type Runtime struct {
	opts               Options
	site               Site
	page               int
	bcookie            string
	cookiesFromBrowser string

	// The browser read happens once per session and its error is cached: the
	// resolve loop retries, and re-reading SQLite/keyring each time would be
	// slow and could return a different failure.
	biliCookieOnce sync.Once
	biliCookie     string
	biliCookieErr  error

	cfgMgr   *config.DmliveConfigManager
	settings *danmaku.DMSettings
	live     *danmaku.LiveDanmaku

	danmakuLn  net.Listener
	videoLn    net.Listener
	danmakuURL string
	videoURL   string
	f2mURL     string

	// tee splits the relay's single-consumer f2m flow between mpv (play)
	// and a recorder ffmpeg (play&record) without either side knowing.
	teeLn  net.Listener
	teeURL string
	tee    *streamTee

	mpv     *mpvctl.Mpv
	mpvDone chan struct{}

	// video danmaku state (bilibili videos)
	lastDanmakuID string
	pageURL       string // override URL with the active ?p= page
	pageMu        sync.Mutex
	lastVideoMsgs []danmaku.VideoDanmaku // kept for the play&record post-mux
	lastVideoMu   sync.Mutex

	// play&record state
	recordDir  string // config record_path or ~/Videos/revda
	recordPath string // stream-record target, fixed at first arm
	recordMu   sync.Mutex

	displayFPS atomic.Uint64
	fpsCounter atomic.Uint64
	lastCmdMS  atomic.Int64
}

// Run starts a dmlive session and blocks until it ends.
func Run(ctx context.Context, opts Options) error {
	site, page, err := DetectSite(opts.URL)
	if err != nil {
		return err
	}
	setLogLevel(opts.LogLevel)
	ffmpegctl.DebugLog = func(line string) { logDebugf("[ffmpeg] %s", line) }

	cfgMgr, err := config.NewDmliveConfigManager()
	if err != nil {
		return err
	}
	cfg := cfgMgr.Get()
	settings := danmaku.NewDMSettings(cfg.DanmakuSpeed, cfg.FontScale, cfg.FontAlpha)
	settings.Quiet.Store(opts.Quiet)

	r := &Runtime{
		opts:               opts,
		site:               site,
		page:               page,
		bcookie:            cfg.BCookie,
		cookiesFromBrowser: cfg.CookiesFromBrowser,
		cfgMgr:             cfgMgr,
		settings:           settings,
		live:               danmaku.NewLiveDanmaku(settings),
		mpvDone:            make(chan struct{}),
		recordDir:          cfg.RecordPath,
	}
	if opts.RecordPath != "" {
		r.recordDir = opts.RecordPath
	}
	if r.danmakuLn, r.videoLn, r.f2mURL, err = newRelaySockets(); err != nil {
		return err
	}
	defer r.danmakuLn.Close()
	defer r.videoLn.Close()
	r.danmakuURL = "tcp://" + r.danmakuLn.Addr().String()
	r.videoURL = "tcp://" + r.videoLn.Addr().String()
	if r.teeLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		return err
	}
	defer r.teeLn.Close()
	r.teeURL = "tcp://" + r.teeLn.Addr().String()
	r.tee = newStreamTee(r.f2mURL)

	logInfof("site %d url %s", site, opts.URL)

	switch {
	case opts.DownloadDM:
		if site != SiteBiliVideo && site != SiteBahaVideo {
			return errors.New("--download-dm supports only bilibili videos and baha")
		}
		return r.downloadDanmaku(ctx)
	case opts.Record || opts.HTTPAddress != "":
		switch site {
		case SiteBahaVideo:
			return errors.New("baha record not supported; use --download-dm")
		case SiteBiliVideo:
			return r.recordBiliVideo(ctx)
		default:
			return r.recordLive(ctx)
		}
	default:
		switch site {
		case SiteBahaVideo:
			return errors.New("baha has no play support; use --download-dm")
		case SiteBiliVideo:
			return r.playVideo(ctx)
		default:
			return r.playLive(ctx)
		}
	}
}

// newRelaySockets binds the danmaku/video listeners and reserves a free f2m
// port (released immediately; ffmpeg's -listen claims it later).
func newRelaySockets() (danmakuLn, videoLn net.Listener, f2mURL string, err error) {
	danmakuLn, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	videoLn, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = danmakuLn.Close()
		return
	}
	f2m, ferr := net.Listen("tcp", "127.0.0.1:0")
	if ferr != nil {
		_ = danmakuLn.Close()
		_ = videoLn.Close()
		err = ferr
		return
	}
	f2mURL = "tcp://" + f2m.Addr().String()
	_ = f2m.Close()
	return
}

// biliCookieValue returns the cookie for bilibili requests: the browser's
// when cookies_from_browser is configured, else the manual bcookie.
func (r *Runtime) biliCookieValue() (string, error) {
	r.biliCookieOnce.Do(func() {
		r.biliCookie, r.biliCookieErr = browsercookies.Resolve(r.bcookie, r.cookiesFromBrowser, ".bilibili.com")
	})
	return r.biliCookie, r.biliCookieErr
}

func (r *Runtime) resolveOnce(ctx context.Context) (*streamfinder.StreamInfo, error) {
	var finder streamfinder.Finder
	switch r.site {
	case SiteBiliLive:
		cookie, err := r.biliCookieValue()
		if err != nil {
			return nil, err
		}
		finder = streamfinder.NewBilibiliFinder(cookie)
	case SiteBiliVideo:
		cookie, err := r.biliCookieValue()
		if err != nil {
			return nil, err
		}
		finder = streamfinder.NewBiliVideoFinder(cookie)
	case SiteBahaVideo:
		finder = streamfinder.NewBahaFinder()
	case SiteDouyinLive:
		finder = streamfinder.NewDouyinFinder()
	case SiteDouyuLive:
		finder = streamfinder.NewDouyuFinder()
	case SiteHuyaLive:
		finder = streamfinder.NewHuyaFinder()
	case SiteTwitchLive:
		finder = streamfinder.NewTwitchFinder()
	case SiteYoutubeLive:
		finder = streamfinder.NewYoutubeFinder()
	default:
		return nil, errors.New("no finder for site")
	}
	target := r.currentURL()
	if r.site == SiteYoutubeLive && strings.Contains(target, "/@") && !strings.Contains(target, "/live") {
		// Channel handles must be pinned to /live or the page may yield a
		// trailer VOD instead of the stream.
		target = strings.TrimRight(target, "/") + "/live"
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return finder.Resolve(resolveCtx, target)
}

// resolve retries like Rust StreamFinder::run: attempts 3s apart (default
// 20), then wait-interval seconds between further rounds until success.
func (r *Runtime) resolve(ctx context.Context) (*streamfinder.StreamInfo, error) {
	attempts := r.opts.ResolveAttempts
	if attempts <= 0 {
		attempts = 20
	}
	for {
		for i := 0; i < attempts; i++ {
			info, err := r.resolveOnce(ctx)
			if err == nil {
				return info, nil
			}
			logInfof("%v", err)
			logWarnf("real url not found, retry...")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
		if r.opts.WaitInterval == 0 {
			break
		}
		logWarnf("waiting for %d seconds...", r.opts.WaitInterval)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(r.opts.WaitInterval) * time.Second):
		}
	}
	return nil, errors.New("max retry, quit")
}

// persist writes the runtime-tuned danmaku settings back to config.toml.
func (r *Runtime) persist() {
	cfg := r.cfgMgr.Get()
	cfg.DanmakuSpeed = r.settings.SpeedMS.Load()
	cfg.FontScale = r.settings.FontScale.Load()
	cfg.FontAlpha = r.settings.FontAlpha.Load()
	_ = r.cfgMgr.Update(cfg)
}

func (r *Runtime) setSpeed(v uint64) {
	if v < 1000 || v >= 30000 { // Rust (1000..30000) is upper-exclusive
		return
	}
	r.settings.SpeedMS.Store(v)
	r.persist()
}

func (r *Runtime) setFontScale(v float64) {
	if v <= 0 {
		return
	}
	r.settings.FontScale.Store(v)
	r.persist()
}

func (r *Runtime) setFontAlpha(v float64) {
	if v < 0 || v > 1 {
		return
	}
	r.settings.FontAlpha.Store(v)
	r.persist()
}

// truncateTitle caps the title at 70 runes and sanitizes path separators,
// matching Rust's char_indices().nth(70) filename rule.
func truncateTitle(s string) string {
	runes := []rune(s)
	if len(runes) > 70 {
		runes = runes[:70]
	}
	return strings.ReplaceAll(string(runes), "/", "-")
}

func recordFilename(title, ext string) string {
	return fmt.Sprintf("%s - %s.%s", truncateTitle(title), time.Now().Format("2006-01-02 15:04:05"), ext)
}
