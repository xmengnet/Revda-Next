package streamfinder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const bahaAPIVideo = "https://api.gamer.com.tw/anime/v1/video.php"

const bahaUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.4 Safari/605.1.15"

// BahaFinder resolves ani.gamer.com.tw anime metadata. Like Rust dmlive it
// carries no video stream support — it fills in the title and the episode's
// videoSn (as DanmakuID) so the danmaku-only paths work.
type BahaFinder struct{}

func NewBahaFinder() *BahaFinder { return &BahaFinder{} }

func (b *BahaFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return nil, err
	}
	sn := u.Query().Get("sn")
	if sn == "" {
		return nil, fmt.Errorf("baha: no sn in url %s", roomURL)
	}

	page := 0
	if p := u.Query().Get("p"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", bahaAPIVideo+"?videoSn="+url.QueryEscape(sn), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", bahaUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Data struct {
			Anime struct {
				Title    string `json:"title"`
				Episodes []struct {
					VideoSn int64 `json:"videoSn"`
				} `json:"episodes"`
			} `json:"anime"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("baha: decode video info: %w", err)
	}
	if body.Data.Anime.Title == "" || len(body.Data.Anime.Episodes) == 0 {
		return nil, fmt.Errorf("baha: no episodes for sn %s", sn)
	}

	episodes := body.Data.Anime.Episodes
	if page < 1 {
		page = 1
	}
	idx := page - 1
	if idx >= len(episodes) {
		idx = len(episodes) - 1
		page = len(episodes)
	}

	title := truncateAtRuneBoundary(body.Data.Anime.Title, len([]byte(body.Data.Anime.Title))-3)
	return &StreamInfo{
		Title:     fmt.Sprintf("%s[%d]", title, page),
		URL:       "https://127.0.0.1", // no video support, mirrors Rust dmlive
		DanmakuID: fmt.Sprintf("%d", episodes[idx].VideoSn),
	}, nil
}

// truncateAtRuneBoundary cuts a byte length while never splitting a UTF-8
// rune (Rust's byte-slice .get(0..n) would return None and error there).
func truncateAtRuneBoundary(s string, n int) string {
	if n < 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
