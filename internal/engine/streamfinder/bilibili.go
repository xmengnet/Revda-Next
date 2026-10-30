package streamfinder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type StreamInfo struct {
	Title    string            `json:"title"`
	URL      string            `json:"url"`
	AudioURL string            `json:"audio_url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	// DanmakuID is the room id (live) or cid (bilibili videos) feeding the
	// danmaku pipeline; empty when the stream has no danmaku.
	DanmakuID string `json:"danmaku_id,omitempty"`
}

type Finder interface {
	Resolve(ctx context.Context, rawURL string) (*StreamInfo, error)
}

type BilibiliFinder struct {
	client *http.Client
	cookie string
}

func NewBilibiliFinder(cookie string) *BilibiliFinder {
	return &BilibiliFinder{
		client: &http.Client{Timeout: 20 * time.Second},
		cookie: cookie,
	}
}

func (b *BilibiliFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("invalid bilibili room url")
	}
	rid := parts[len(parts)-1]

	apiURL := fmt.Sprintf("https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo?room_id=%s&protocol=0,1&format=0,1,2&codec=0,1&qn=20000&platform=web&ptype=8&dolby=5&panorama=1", rid)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", roomURL)
	if b.cookie != "" {
		req.Header.Set("Cookie", b.cookie)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res struct {
		Code int `json:"code"`
		Data struct {
			PlayURLInfo struct {
				PlayURL struct {
					Stream []struct {
						Format []struct {
							Codec []struct {
								BaseURL string `json:"base_url"`
								URLInfo []struct {
									Host  string `json:"host"`
									Extra string `json:"extra"`
								} `json:"url_info"`
							} `json:"codec"`
						} `json:"format"`
					} `json:"stream"`
				} `json:"playurl"`
			} `json:"playurl_info"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("bilibili parse json error: %w", err)
	}

	streams := res.Data.PlayURLInfo.PlayURL.Stream
	if len(streams) == 0 || len(streams[0].Format) == 0 || len(streams[0].Format[0].Codec) == 0 {
		return nil, Offline("bilibili returned no live streams")
	}

	codec := streams[0].Format[0].Codec[0]
	if len(codec.URLInfo) == 0 {
		return nil, errors.New("empty url info for bilibili stream")
	}

	streamURL := fmt.Sprintf("%s%s%s", codec.URLInfo[0].Host, codec.BaseURL, codec.URLInfo[0].Extra)

	headers := map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"Referer":    "https://live.bilibili.com/",
	}

	title := fetchBilibiliRoomTitle(ctx, b.client, b.cookie, rid)
	if title == "" {
		title = "Bilibili Live " + rid
	}

	return &StreamInfo{
		Title:   title,
		URL:     streamURL,
		Headers: headers,
	}, nil
}

// fetchBilibiliRoomTitle is best-effort: the playurl API does not carry the
// room title, so ask Room/get_info separately and fall back to the caller's
// placeholder on any failure.
func fetchBilibiliRoomTitle(ctx context.Context, client *http.Client, cookie, rid string) string {
	titleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(titleCtx, "GET",
		"https://api.live.bilibili.com/room/v1/Room/get_info?room_id="+rid, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://live.bilibili.com/")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var res struct {
		Code int `json:"code"`
		Data struct {
			Title string `json:"title"`
		} `json:"data"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(body, &res) != nil || res.Code != 0 {
		return ""
	}
	return strings.TrimSpace(res.Data.Title)
}
