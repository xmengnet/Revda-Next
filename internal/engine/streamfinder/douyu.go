package streamfinder

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

//go:embed crypto-js.min.js
var cryptoJS string

const (
	douyuHomeH5Enc = "https://www.douyu.com/swf_api/homeH5Enc?rids="
	douyuGetH5Play = "https://www.douyu.com/lapi/live/getH5Play/"
)

type DouyuFinder struct {
	client *http.Client
}

func NewDouyuFinder() *DouyuFinder {
	return &DouyuFinder{
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (d *DouyuFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("invalid douyu room url")
	}
	rid := parts[len(parts)-1]

	// 1. Fetch JS encryption code
	encReq, err := http.NewRequestWithContext(ctx, "GET", douyuHomeH5Enc+rid, nil)
	if err != nil {
		return nil, err
	}
	encReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	encReq.Header.Set("Referer", fmt.Sprintf("https://www.douyu.com/%s", rid))

	encResp, err := d.client.Do(encReq)
	if err != nil {
		return nil, err
	}
	defer encResp.Body.Close()

	encBody, err := io.ReadAll(encResp.Body)
	if err != nil {
		return nil, err
	}

	var encResult struct {
		Error int               `json:"error"`
		Msg   string            `json:"msg"`
		Data  map[string]string `json:"data"`
	}
	if err := json.Unmarshal(encBody, &encResult); err != nil {
		return nil, fmt.Errorf("douyu parse h5 enc json error: %w", err)
	}

	jsEnc := encResult.Data["room"+rid]
	if jsEnc == "" {
		return nil, fmt.Errorf("douyu room%s js encryption not found", rid)
	}

	// 2. Execute JS with goja
	vm := goja.New()
	if _, err := vm.RunString(cryptoJS); err != nil {
		return nil, fmt.Errorf("eval crypto-js error: %w", err)
	}
	if _, err := vm.RunString(jsEnc); err != nil {
		return nil, fmt.Errorf("eval room js error: %w", err)
	}

	didBytes := make([]byte, 16)
	for i := range didBytes {
		didBytes[i] = byte(rand.IntN(256))
	}
	did := hex.EncodeToString(didBytes)
	tsec := fmt.Sprintf("%d", time.Now().Unix())

	evalCall := fmt.Sprintf("ub98484234('%s','%s','%s')", rid, did, tsec)
	val, err := vm.RunString(evalCall)
	if err != nil {
		return nil, fmt.Errorf("eval ub98484234 error: %w", err)
	}
	encParamsStr := val.String()

	// 3. Post to getH5Play
	form := url.Values{}
	for _, pair := range strings.Split(encParamsStr, "&") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			form.Set(k, v)
		}
	}
	form.Set("cdn", "")
	form.Set("iar", "0")
	form.Set("ive", "0")
	form.Set("rate", "0")

	playReq, err := http.NewRequestWithContext(ctx, "POST", douyuGetH5Play+rid, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	playReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	playReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	playReq.Header.Set("Referer", fmt.Sprintf("https://www.douyu.com/%s", rid))

	playResp, err := d.client.Do(playReq)
	if err != nil {
		return nil, err
	}
	defer playResp.Body.Close()

	playBody, err := io.ReadAll(playResp.Body)
	if err != nil {
		return nil, err
	}

	var playResult struct {
		Error int `json:"error"`
		Data  struct {
			RtmpURL  string `json:"rtmp_url"`
			RtmpLive string `json:"rtmp_live"`
		} `json:"data"`
	}
	if err := json.Unmarshal(playBody, &playResult); err != nil {
		return nil, fmt.Errorf("douyu parse h5 play json error: %w", err)
	}

	if playResult.Data.RtmpURL == "" || playResult.Data.RtmpLive == "" {
		return nil, Offline("douyu returned no stream")
	}

	streamURL := fmt.Sprintf("%s/%s", playResult.Data.RtmpURL, playResult.Data.RtmpLive)

	title := fetchDouyuRoomTitle(ctx, d.client, rid)
	if title == "" {
		title = "Douyu Live " + rid
	}

	return &StreamInfo{
		Title: title,
		URL:   streamURL,
		Headers: map[string]string{
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
			"Referer":    "https://www.douyu.com/",
		},
	}, nil
}

// fetchDouyuRoomTitle is best-effort: the signed getH5Play response does not
// carry the room name, so ask the unsigned betard room API and fall back to
// the caller's placeholder on any failure.
func fetchDouyuRoomTitle(ctx context.Context, client *http.Client, rid string) string {
	titleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(titleCtx, "GET",
		"https://www.douyu.com/betard/"+rid, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.douyu.com/"+rid)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var res struct {
		Room struct {
			RoomName string `json:"room_name"`
			Nickname string `json:"nickname"`
		} `json:"room"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || json.Unmarshal(body, &res) != nil {
		return ""
	}
	if res.Room.RoomName != "" {
		return res.Room.RoomName
	}
	return res.Room.Nickname
}
