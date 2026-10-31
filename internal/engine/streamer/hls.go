package streamer

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// m3u8 is one parsed live playlist.
type m3u8 struct {
	sequence       uint64
	targetDuration uint64
	clips          []MediaSegment
	streams        []variantStream // variant playlists, bandwidth-sorted by caller
}

type variantStream struct {
	bandwidth int
	url       string
}

// DecodeM3U8 parses a live playlist. Segments whose EXTINF mentions "Amazon"
// are ads: downloaded to keep the sequence intact but not written to the
// output (skip=1), exactly like Rust dmlive.
func DecodeM3U8(text string) *m3u8 {
	out := &m3u8{sequence: 0, targetDuration: 5}
	extinf := ""
	extStreamInf := ""
	header := ""

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			k, v, found := strings.Cut(strings.TrimPrefix(line, "#"), ":")
			if !found {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "EXT-X-MEDIA-SEQUENCE":
				out.sequence, _ = strconv.ParseUint(v, 10, 64)
			case "EXT-X-TARGETDURATION":
				out.targetDuration, _ = strconv.ParseUint(v, 10, 64)
			case "EXT-X-MAP":
				if _, attr, ok := strings.Cut(v, "="); ok {
					h := strings.Trim(strings.TrimSpace(attr), `"`)
					header = h
				}
			case "EXTINF":
				extinf = v
			case "EXT-X-STREAM-INF":
				extStreamInf = v
			}
			continue
		}
		if extStreamInf == "" {
			skip := 0
			if strings.Contains(extinf, "Amazon") {
				skip = 1
			}
			out.clips = append(out.clips, MediaSegment{Skip: skip, URL: line})
			extinf = ""
		} else {
			bw := 1
			for _, part := range strings.Split(extStreamInf, ",") {
				if strings.HasPrefix(strings.TrimSpace(part), "BANDWIDTH") {
					if _, val, ok := strings.Cut(part, "="); ok {
						if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
							bw = n
						}
					}
				}
			}
			out.streams = append(out.streams, variantStream{bandwidth: bw, url: line})
			out.targetDuration = 1
			out.sequence = 0
		}
	}
	if header != "" {
		out.clips = append([]MediaSegment{{Skip: 1, IsHeader: true, URL: header}}, out.clips...)
	}
	return out
}

// HLS relays a live HLS playlist onto the video socket, refreshing the
// manifest and skipping stale segments on startup.
type HLS struct {
	baseURL    string
	referer    string
	maxWaiting int // watchdog tolerance in seconds (twitch 30, others 10)

	client *http.Client

	headerDone  bool
	streamReady bool
	watchdog    bool // set true after each successful segment download
}

func NewHLS(baseURL, referer string, twitch bool) *HLS {
	maxWaiting := 10
	if twitch {
		maxWaiting = 30
	}
	return &HLS{
		baseURL:    baseURL,
		referer:    referer,
		maxWaiting: maxWaiting,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// parseClipURL resolves a segment reference against the playlist URL and
// passes through the playlist's query when the result has none.
func (h *HLS) parseClipURL(clip string) string {
	if strings.HasPrefix(clip, "http") {
		return clip
	}
	base, err := url.Parse(h.baseURL)
	if err != nil {
		return clip
	}
	ref, err := base.Parse(clip)
	if err != nil {
		return clip
	}
	if !strings.Contains(ref.String(), "?") {
		return ref.String() + "?" + base.RawQuery
	}
	return ref.String()
}

func (h *HLS) get(ctx context.Context, u string, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", GenUA)
	req.Header.Set("Connection", "keep-alive")
	if h.referer != "" {
		req.Header.Set("Referer", h.referer)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Run refreshes the manifest, downloads segments in order and relays them to
// the consumer on videoLn until the watchdog gives up or ctx ends.
func (h *HLS) Run(ctx context.Context, videoLn net.Listener, onReady func()) error {
	conn, err := AcceptConsumer(ctx, videoLn)
	if err != nil {
		return err
	}
	defer conn.Close()

	ss := NewSegmentStream()
	stop := make(chan struct{})
	defer close(stop)

	downloadErr := make(chan error, 1)
	go func() { downloadErr <- h.downloadTask(ctx, conn, ss, onReady) }()
	refreshErr := make(chan error, 1)
	go func() { refreshErr <- h.refreshTask(ctx, ss) }()

	watchdogErr := make(chan error, 1)
	segDone := make(chan struct{})
	go func() {
		defer close(segDone)
		cnt := 0
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if !h.watchdog {
				cnt++
			} else {
				cnt = 0
			}
			if cnt > h.maxWaiting {
				watchdogErr <- fmt.Errorf("hls: watch dog failed")
				return
			}
			h.watchdog = false
		}
	}()

	go ss.Run(stop)

	select {
	case err := <-downloadErr:
		return err
	case err := <-refreshErr:
		return err
	case err := <-watchdogErr:
		if err == context.Canceled {
			return err
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *HLS) downloadTask(ctx context.Context, conn net.Conn, ss *SegmentStream, onReady func()) error {
	for {
		var clip MediaSegment
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c, ok := <-ss.ClipCh():
			if !ok {
				return nil
			}
			clip = c
		}
		if !h.headerDone && clip.IsHeader {
			clip.Skip = 0
			h.headerDone = true
		} else if clip.Skip == 2 {
			continue
		}
		u := h.parseClipURL(clip.URL)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(cctx, "GET", u, nil)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("User-Agent", GenUA)
		req.Header.Set("Connection", "keep-alive")
		if h.referer != "" {
			req.Header.Set("Referer", h.referer)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			cancel()
			return err
		}
		buf := make([]byte, 64*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 && clip.Skip == 0 {
				if !h.streamReady {
					h.streamReady = true
					if onReady != nil {
						onReady()
					}
				}
				if _, werr := conn.Write(buf[:n]); werr != nil {
					resp.Body.Close()
					cancel()
					return werr
				}
			}
			if rerr != nil {
				break
			}
		}
		resp.Body.Close()
		cancel()
		h.watchdog = true
	}
}

func (h *HLS) refreshTask(ctx context.Context, ss *SegmentStream) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ss.RefreshCh():
			if !ok {
				return nil
			}
		}
		text, err := h.get(ctx, h.baseURL, time.Duration(ss.RefreshIntervalMs())*time.Millisecond)
		if err != nil {
			continue // refresh failures are tolerated, like Rust
		}
		m := DecodeM3U8(text)
		if len(m.streams) > 0 {
			best := m.streams[0]
			for _, s := range m.streams {
				if s.bandwidth > best.bandwidth {
					best = s
				}
			}
			h.baseURL = h.parseClipURL(best.url)
		}
		ss.UpdateSequence(m.sequence, m.clips, m.targetDuration*1000)
	}
}
