package danmaku

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// bahaAPIDanmu dumps an episode's complete danmaku list.
const bahaAPIDanmu = "https://api.gamer.com.tw/anime/v1/danmu.php?geo=TW%2CHK&videoSn="

const bahaUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.4 Safari/605.1.15"

// FetchBahaDanmaku downloads all danmaku of one baha episode (sn). Times come
// in deciseconds and are scaled to milliseconds; non-zero positions are fixed
// top text, matching Rust dmlive's mapping.
func FetchBahaDanmaku(ctx context.Context, client *http.Client, sn string) ([]VideoDanmaku, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", bahaAPIDanmu+url.QueryEscape(sn), nil)
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
			Danmu []struct {
				Text     string `json:"text"`
				Time     int64  `json:"time"` // deciseconds
				Position int64  `json:"position"`
				Color    string `json:"color"`
			} `json:"danmu"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("baha: decode danmu: %w", err)
	}

	msgs := make([]VideoDanmaku, 0, len(body.Data.Danmu))
	for _, d := range body.Data.Danmu {
		color := strings.TrimPrefix(d.Color, "#")
		if color == "" {
			color = "FFFFFF"
		}
		msgs = append(msgs, VideoDanmaku{
			Offset: time.Duration(d.Time*100) * time.Millisecond,
			Text:   strings.TrimSpace(d.Text),
			Color:  strings.ToLower(color),
			Top:    d.Position != 0,
		})
	}
	return msgs, nil
}
