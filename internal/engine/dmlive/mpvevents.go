package dmlive

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// dmlCmd is the parsed form of a "dml:..." script-message sent by the mpv
// keybinds (Rust mpv/cmdparser.rs).
type dmlCmd struct {
	restart bool
	next    bool
	back    bool
	fsup    bool
	fsdown  bool
	nick    bool
	fps     bool

	fs    *float64
	fa    *float64
	speed *uint64
	page  *uint64
}

func parseDmlCmd(s string) dmlCmd {
	var c dmlCmd
	if !strings.HasPrefix(s, "dml:") {
		return c
	}
	for _, raw := range strings.Split(s[4:], ",") {
		cmd := strings.TrimSpace(raw)
		switch cmd {
		case "r", "reload":
			c.restart = true
			continue
		case "next":
			c.next = true
			continue
		case "back":
			c.back = true
			continue
		case "fsup":
			c.fsup = true
			continue
		case "fsdown":
			c.fsdown = true
			continue
		case "nick":
			c.nick = true
			continue
		case "fps":
			c.fps = true
			continue
		}
		k, v, found := strings.Cut(cmd, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(k) {
		case "fs":
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				c.fs = &f
			}
		case "fa":
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				c.fa = &f
			}
		case "speed":
			if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
				c.speed = &n
			}
		case "p", "page":
			if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
				c.page = &n
			}
		}
	}
	return c
}

// handleMpvEvents drives the mpv-side control loop: keybind script messages,
// per-file probes (video params / fps) and page auto-advance.
func (r *Runtime) handleMpvEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-r.mpv.Events():
			if !ok {
				return
			}
			switch ev.Name {
			case "client-message":
				if len(ev.Args) > 0 {
					r.handleDmlCmd(ctx, ev.Args[0])
				}
			case "file-loaded":
				go r.onFileLoaded(ctx)
			case "end-file":
				var e struct {
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(ev.Raw, &e)
				if r.site == SiteBiliVideo && e.Reason == "eof" {
					page := r.page + 1
					go r.loadVideoPage(ctx, page)
				}
			}
		}
	}
}

// onFileLoaded probes the just-loaded file. Bilibili videos get their
// danmaku track mounted here; every site gets the fps adaptation probe.
func (r *Runtime) onFileLoaded(ctx context.Context) {
	time.Sleep(time.Second) // let mpv settle, like Rust's 1000ms
	if r.mpv == nil || ctx.Err() != nil {
		return
	}

	if r.site == SiteBiliVideo {
		if w, h, ok := r.videoParams(); ok && w > 0 && h > 0 {
			ratio := 16.0 * float64(h) / float64(w) / 9.0
			go r.serveVideoDanmaku(ctx, r.lastDanmakuID, ratio)
			_ = r.mpv.Command("sub-remove", "1")
			_ = r.mpv.Command("sub-add", r.danmakuURL, "select")
			_ = r.mpv.Command("set_property", "sub-visibility", true)
		}
	}

	if df, err := r.mpv.GetPropertyFloat("display-fps"); err == nil {
		r.displayFPS.Store(uint64(math.Round(df)))
	}
	if cf, err := r.mpv.GetPropertyFloat("container-fps"); err == nil {
		if r.fpsCounter.Load() == 0 && cf < 59.0 {
			_ = r.mpv.Command("set_property", "vf", "fps=fps=60:round=near")
		}
	}
}

func (r *Runtime) videoParams() (w, h int, ok bool) {
	data, err := r.mpv.GetProperty("video-params")
	if err != nil {
		return 0, 0, false
	}
	var vp struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	if json.Unmarshal(data, &vp) != nil {
		return 0, 0, false
	}
	return vp.W, vp.H, true
}

// handleDmlCmd applies one keybind command with dmlive's 1s rate limit.
func (r *Runtime) handleDmlCmd(ctx context.Context, s string) {
	now := time.Now().UnixMilli()
	if now-r.lastCmdMS.Load() < 1000 {
		return
	}
	r.lastCmdMS.Store(now)

	cmd := parseDmlCmd(s)

	if cmd.restart {
		if r.site == SiteBiliVideo {
			go r.loadVideoPage(ctx, r.page)
		} else {
			_ = r.mpv.Command("stop")
		}
	}
	switch {
	case cmd.fs != nil:
		r.setFontScale(*cmd.fs)
	case cmd.fsup:
		r.setFontScale(r.settings.FontScale.Load() + 0.15)
	case cmd.fsdown:
		r.setFontScale(r.settings.FontScale.Load() - 0.15)
	}
	if cmd.fa != nil {
		r.setFontAlpha(*cmd.fa)
	}
	if cmd.speed != nil {
		r.setSpeed(*cmd.speed)
	}
	if cmd.page != nil {
		go r.loadVideoPage(ctx, int(*cmd.page))
	}
	if cmd.nick {
		r.settings.ShowNick.Store(!r.settings.ShowNick.Load())
	}
	if cmd.back {
		p := r.page - 1
		if p < 1 {
			p = 1
		}
		go r.loadVideoPage(ctx, p)
	}
	if cmd.next {
		go r.loadVideoPage(ctx, r.page+1)
	}
	if cmd.fps {
		r.cycleFPS()
	}
}

// cycleFPS rounds the forced fps filter through [display, off, 60] like
// Rust's alt+f handler.
func (r *Runtime) cycleFPS() {
	candidates := [3]uint64{r.displayFPS.Load(), 0, 60}
	fps := candidates[r.fpsCounter.Load()%3]
	r.fpsCounter.Add(1)
	if fps == 0 {
		_ = r.mpv.Command("set_property", "vf", "")
		return
	}
	_ = r.mpv.Command("set_property", "vf", fmt.Sprintf("fps=fps=%d:round=near", fps))
}
