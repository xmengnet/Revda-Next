package danmaku

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const apiVideoDanmaku = "https://api.bilibili.com/x/v1/dm/list.so?oid="

// VideoDanmaku is one danmaku of a regular bilibili video. Offset is the
// video position (not wall clock), so the whole batch renders as one static
// ASS document that maps 1:1 onto the playback timeline.
type VideoDanmaku struct {
	Offset time.Duration
	Text   string
	Color  string // hex RRGGBB, "" = default white
	Top    bool   // mode 5: fixed top text
	Bottom bool   // mode 4: fixed bottom text
}

// FetchVideoDanmaku downloads and parses the complete danmaku XML of a video
// (oid = cid). The response is deflate-compressed XML.
func FetchVideoDanmaku(ctx context.Context, client *http.Client, cid string) ([]VideoDanmaku, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, "GET", apiVideoDanmaku+cid, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}

	doc, err := decodeVideoDanmakuXML(raw)
	if err != nil {
		return nil, fmt.Errorf("bilibili video danmaku xml: %w", err)
	}

	msgs := make([]VideoDanmaku, 0, len(doc.D))
	for _, d := range doc.D {
		fields := strings.Split(d.P, ",")
		if len(fields) < 4 {
			continue
		}
		sec, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || sec < 0 {
			continue
		}
		mode, _ := strconv.Atoi(fields[1])
		if mode == 7 || mode == 8 || mode == 9 { // special / code / BTS: skip
			continue
		}
		color := ""
		if cv, cerr := strconv.ParseInt(fields[3], 10, 64); cerr == nil && cv > 0 && cv != 0xFFFFFF {
			color = fmt.Sprintf("%06x", cv)
		}
		msgs = append(msgs, VideoDanmaku{
			Offset: time.Duration(sec * float64(time.Second)),
			Text:   strings.TrimSpace(d.Text),
			Color:  color,
			Bottom: mode == 4,
			Top:    mode == 5,
		})
	}
	return msgs, nil
}

type videoDanmakuXML struct {
	D []struct {
		P    string `xml:"p,attr"`
		Text string `xml:",chardata"`
	} `xml:"d"`
}

// decodeVideoDanmakuXML handles the list.so quirks: the body is normally
// zlib-compressed, but the endpoint sometimes answers with plain or raw
// deflate depending on CDN node.
func decodeVideoDanmakuXML(raw []byte) (videoDanmakuXML, error) {
	var doc videoDanmakuXML
	try := func(r io.Reader) error {
		return xml.NewDecoder(r).Decode(&doc)
	}
	if zr, zerr := zlib.NewReader(bytes.NewReader(raw)); zerr == nil {
		if err := try(zr); err == nil {
			return doc, nil
		} else {
			_ = zr.Close()
		}
	}
	if err := try(flate.NewReader(bytes.NewReader(raw))); err == nil {
		return doc, nil
	}
	if err := try(bytes.NewReader(raw)); err == nil {
		return doc, nil
	}
	return doc, fmt.Errorf("undecodable danmaku payload (%d bytes, head %.16x)", len(raw), raw)
}

// RenderVideoASS renders the complete ASS document for a video: timestamps
// are video positions, lanes are assigned in chronological order, and the
// result never changes afterwards (single mount, no reload).
func RenderVideoASS(msgs []VideoDanmaku, fontScale, fontAlpha float64, speedMs int, areaPct int, anchor string) string {
	if fontScale <= 0 {
		fontScale = 1.0
	}
	if fontAlpha < 0 || fontAlpha > 1 {
		fontAlpha = 0
	}
	speedSec := speedMs / 1000
	if speedSec < 4 {
		speedSec = 8
	} else if speedSec > 30 {
		speedSec = 30
	}
	fontSize := int(40 * fontScale)
	numLanes := danmakuChannelNum(fontSize, areaPct)
	baseY := danmakuBaseY(clampDanmakuArea(areaPct), anchorIndex(anchor))

	sorted := make([]VideoDanmaku, len(msgs))
	copy(sorted, msgs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })

	laneBusy := make([]time.Duration, numLanes)

	var b strings.Builder
	fmt.Fprintf(&b, assHeader, fontSize, int(fontAlpha*255))
	alphaTag := fmt.Sprintf("{\\alpha&H%02X&}", int(fontAlpha*255))

	for _, m := range sorted {
		text := CleanDanmakuText(m.Text)
		if text == "" {
			continue
		}
		colorTag := ""
		if m.Color != "" && len(m.Color) == 6 {
			c := strings.ToLower(m.Color)
			colorTag = fmt.Sprintf("{\\1c&H%s%s%s&}", c[4:6], c[2:4], c[0:2])
		}
		startStr := formatASSTime(m.Offset.Seconds())
		endStr := formatASSTime(m.Offset.Seconds() + float64(speedSec))

		if m.Top || m.Bottom {
			align := 8 // top-center
			if m.Bottom {
				align = 2
			}
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s%s{\\an%d}%s\n",
				startStr, endStr, alphaTag, colorTag, align, escapeASSText(text))
			continue
		}

		lane := -1
		for i := range laneBusy {
			if m.Offset >= laneBusy[i] {
				lane = i
				break
			}
		}
		if lane == -1 {
			lane = int(m.Offset.Nanoseconds()) % numLanes
		}
		textWidth := float64(len([]rune(text))) * float64(fontSize)
		busy := time.Duration(float64(speedSec) * (textWidth/1920.0 + 0.15) * float64(time.Second))
		laneBusy[lane] = m.Offset + busy

		y := baseY + lane*fontSize
		endX := -(int(textWidth) + 50)
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s%s{\\move(1920,%d,%d,%d)}%s\n",
			startStr, endStr, alphaTag, colorTag, y, endX, y, escapeASSText(text))
	}
	return b.String()
}

// StartStaticHTTP serves one fixed ASS document with a known Content-Length —
// the same snapshot contract as StartHTTP, but the body never changes so the
// player mounts it once and no reload loop is needed.
func StartStaticHTTP(ctx context.Context, ass string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/danmaku.ass", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/x-ssa")
		w.Header().Set("Content-Length", fmt.Sprint(len(ass)))
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write([]byte(ass))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() { _ = srv.Serve(ln) }()
	return fmt.Sprintf("http://%s/danmaku.ass", ln.Addr().String()), nil
}
