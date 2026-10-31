package streamfinder

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

//go:embed douyin_abogus.js
var douyinABogusJS string

const (
	douyinUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"
	douyinEnterURL = "https://live.douyin.com/webcast/room/web/enter/"
	douyinHomeURL  = "https://live.douyin.com/"
)

type DouyinFinder struct {
	client *http.Client
}

func NewDouyinFinder() *DouyinFinder {
	return &DouyinFinder{
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// douyinSession carries the anonymous visitor cookies the douyin web APIs
// require: ttwid (device id issued by live.douyin.com), __ac_nonce and the
// __ac_signature derived from it. No login account is involved.
type douyinSession struct {
	ttwid   string
	acNonce string
	acSign  string
	msToken string
}

// Resolve turns a https://live.douyin.com/{web_rid} URL into a pull stream.
// Flow (mirrors DouyinLiveWebFetcher): bootstrap anonymous cookies, then ask
// the room/web/enter API (signed with a_bogus) for the flv/hls pull URLs.
// The enter API accepts the public web_rid alone; no login is involved.
func (f *DouyinFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	rid, err := douyinRidFromURL(roomURL)
	if err != nil {
		return nil, err
	}

	sess, err := f.newSession(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := f.enterRoom(ctx, sess, rid)
	if err != nil {
		return nil, err
	}

	room, ok := resp.pickRoom()
	if !ok {
		return nil, Offline("douyin room is not live")
	}
	streamURL := room.pickStream()
	if streamURL == "" {
		return nil, Offline("douyin returned no stream")
	}

	title := room.Title
	if title == "" {
		title = "Douyin Live " + rid
	}

	return &StreamInfo{
		Title: title,
		URL:   streamURL,
		Headers: map[string]string{
			"User-Agent": douyinUA,
			"Referer":    douyinHomeURL,
		},
		DanmakuID: rid,
	}, nil
}

func douyinRidFromURL(roomURL string) (string, error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "", errors.New("invalid douyin room url")
	}
	return parts[len(parts)-1], nil
}

func (f *DouyinFinder) newSession(ctx context.Context) (*douyinSession, error) {
	sess := &douyinSession{msToken: douyinMsToken()}

	// ttwid (and usually __ac_nonce) come with the live homepage response.
	if err := f.collectCookies(ctx, sess, douyinHomeURL); err != nil {
		return nil, err
	}
	if sess.acNonce == "" {
		if err := f.collectCookies(ctx, sess, "https://www.douyin.com/"); err != nil {
			return nil, err
		}
	}
	if sess.ttwid == "" {
		return nil, errors.New("douyin: no ttwid cookie issued")
	}
	if sess.acNonce == "" {
		// DouyinLiveWebFetcher ships a fixed nonce that the enter API accepts.
		sess.acNonce = "0123407cc00a9e438deb4"
	}
	sess.acSign = douyinAcSignature("www.douyin.com/", sess.acNonce, douyinUA, time.Now().Unix())
	return sess, nil
}

func (f *DouyinFinder) collectCookies(ctx context.Context, sess *douyinSession, pageURL string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", douyinUA)

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	for _, ck := range resp.Cookies() {
		switch ck.Name {
		case "ttwid":
			sess.ttwid = ck.Value
		case "__ac_nonce":
			sess.acNonce = ck.Value
		}
	}
	return nil
}

type douyinEnterResp struct {
	StatusCode int `json:"status_code"`
	Data       struct {
		RoomStatus int `json:"room_status"` // 0 live, 2 ended
		User       struct {
			Nickname string `json:"nickname"`
		} `json:"user"`
		Rooms []douyinRoomData `json:"data"`
		// SimilarRooms are recommended channels, usually currently live; they
		// are a convenient source of live room ids when discovery is needed.
		SimilarRooms []struct {
			WebRid string `json:"web_rid"`
		} `json:"similar_rooms"`
	} `json:"data"`
}

type douyinRoomData struct {
	IdStr  string `json:"id_str"`
	Title  string `json:"title"`
	Status int    `json:"status"` // 2 live
	Cover  struct {
		URLList []string `json:"url_list"`
	} `json:"cover"`
	Stream struct {
		RtmpPullURL string            `json:"rtmp_pull_url"`
		HlsPullURL  string            `json:"hls_pull_url"`
		FlvPullURL  map[string]string `json:"flv_pull_url"`
	} `json:"stream_url"`
}

// DouyinRoomMeta is the room metadata the enter API carries, for callers that
// only need title/cover/live-state (the room list) without a pull URL.
type DouyinRoomMeta struct {
	Title    string
	Owner    string
	Cover    string
	IsLiving bool
}

// RoomMeta queries the enter API for room metadata only. It shares the
// anonymous session and a_bogus signing with Resolve.
func (f *DouyinFinder) RoomMeta(ctx context.Context, webRid string) (*DouyinRoomMeta, error) {
	sess, err := f.newSession(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := f.enterRoom(ctx, sess, webRid)
	if err != nil {
		return nil, err
	}
	if len(resp.Data.Rooms) == 0 {
		return nil, fmt.Errorf("douyin room %s not found", webRid)
	}
	room := resp.Data.Rooms[0]

	cover := ""
	if len(room.Cover.URLList) > 0 {
		cover = room.Cover.URLList[0]
	}
	title := room.Title
	if title == "" {
		title = "没有直播标题"
	}
	return &DouyinRoomMeta{
		Title:    title,
		Owner:    resp.Data.User.Nickname,
		Cover:    cover,
		IsLiving: room.Status == 2 || room.pickStream() != "",
	}, nil
}

func (r *douyinEnterResp) pickRoom() (douyinRoomData, bool) {
	for _, room := range r.Data.Rooms {
		if room.Stream.RtmpPullURL != "" || room.Stream.HlsPullURL != "" || len(room.Stream.FlvPullURL) > 0 {
			return room, true
		}
	}
	return douyinRoomData{}, false
}

// pickStream prefers the muxed FLV variants, highest quality first.
func (r *douyinRoomData) pickStream() string {
	for _, q := range []string{"FULL_HD1", "HD1", "SD2", "SD1", "HS1"} {
		if v, ok := r.Stream.FlvPullURL[q]; ok && v != "" {
			return v
		}
	}
	for _, v := range r.Stream.FlvPullURL {
		if v != "" {
			return v
		}
	}
	if r.Stream.RtmpPullURL != "" {
		return r.Stream.RtmpPullURL
	}
	return r.Stream.HlsPullURL
}

func (f *DouyinFinder) enterRoom(ctx context.Context, sess *douyinSession, webRid string) (*douyinEnterResp, error) {
	q := url.Values{}
	q.Set("aid", "6383")
	q.Set("app_name", "douyin_web")
	q.Set("live_id", "1")
	q.Set("device_platform", "web")
	q.Set("language", "zh-CN")
	q.Set("enter_from", "page_refresh")
	q.Set("cookie_enabled", "true")
	q.Set("screen_width", "1920")
	q.Set("screen_height", "1080")
	q.Set("browser_language", "zh-CN")
	q.Set("browser_platform", "Win32")
	q.Set("browser_name", "Edge")
	q.Set("browser_version", "140.0.0.0")
	q.Set("web_rid", webRid)
	q.Set("enter_source", "")
	q.Set("is_need_double_stream", "false")
	q.Set("insert_task_id", "")
	q.Set("live_reason", "")
	q.Set("msToken", sess.msToken)
	query := q.Encode()

	cookie := fmt.Sprintf("ttwid=%s; __ac_nonce=%s; __ac_signature=%s; msToken=%s",
		sess.ttwid, sess.acNonce, sess.acSign, sess.msToken)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		ab, err := douyinABogus(query, douyinUA)
		if err != nil {
			lastErr = err
			continue
		}
		target := douyinEnterURL + "?" + query + "&a_bogus=" + ab

		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", douyinUA)
		req.Header.Set("Referer", douyinHomeURL+webRid)
		req.Header.Set("Cookie", cookie)

		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		var result douyinEnterResp
		if json.Unmarshal(body, &result) != nil {
			lastErr = fmt.Errorf("douyin enter: bad json (%d bytes, http %d)", len(body), resp.StatusCode)
			continue
		}
		if result.StatusCode == 0 {
			return &result, nil
		}
		// a_bogus is flaky by design; regenerating usually fixes it.
		lastErr = fmt.Errorf("douyin enter status_code=%d", result.StatusCode)
	}
	return nil, lastErr
}

func douyinABogus(query, ua string) (string, error) {
	vm := goja.New()
	if _, err := vm.RunString(douyinABogusJS); err != nil {
		return "", fmt.Errorf("douyin a_bogus js init: %w", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("get_ab"))
	if !ok {
		return "", errors.New("douyin a_bogus js: get_ab missing")
	}
	v, err := fn(goja.Undefined(), vm.ToValue(query), vm.ToValue(ua))
	if err != nil {
		return "", fmt.Errorf("douyin a_bogus calc: %w", err)
	}
	if s := v.String(); s != "" {
		return s, nil
	}
	return "", errors.New("douyin a_bogus empty")
}

const douyinMsTokenLen = 182

func douyinMsToken() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	buf := make([]byte, douyinMsTokenLen)
	for i := range buf {
		buf[i] = charset[rand.IntN(len(charset))]
	}
	return string(buf)
}

// douyinAcSignature is a Go port of the __ac_signature algorithm
// (DouyinLiveWebFetcher ac_signature.py): a 65599-rolling-hash based
// checksum over timestamp, site, nonce and user agent.
func douyinAcSignature(site, nonce, ua string, ts int64) string {
	calOneStr := func(s string, k uint32) uint32 {
		for _, ch := range s {
			k = ((k ^ uint32(ch)) * 65599) & 0xFFFFFFFF
		}
		return k
	}
	calOneStr3 := func(s string, k uint32) uint32 {
		for _, ch := range s {
			k = (k*65599 + uint32(ch)) & 0xFFFFFFFF
		}
		return k
	}
	encChr := func(c uint32) byte {
		switch {
		case c < 26:
			return byte(c + 65)
		case c < 52:
			return byte(c + 71)
		case c < 62:
			return byte(c - 4)
		default:
			return byte(c - 17)
		}
	}
	encNum := func(v uint32) string {
		out := make([]byte, 0, 4)
		for i := 24; i >= 0; i -= 6 {
			out = append(out, encChr((v>>uint(i))&63))
		}
		return string(out)
	}

	const signHead = "_02B4Z6wo00f01"
	tsStr := strconv.FormatInt(ts, 10)

	a := calOneStr(site, calOneStr(tsStr, 0)) % 65521
	x := uint32(ts) ^ (a * 65521)
	b := (uint64(0b10000000110000) << 32) | uint64(x)
	c := calOneStr(strconv.FormatUint(b, 10), 0)
	e := uint32(b >> 32)

	d := encNum(uint32(b >> 2))
	f := encNum(uint32((b << 28) | uint64(e>>4)))
	g := 582085784 ^ uint32(b)
	h := encNum((e << 26) | (g >> 6))
	i := encChr(g & 63)

	j := ((calOneStr(ua, c) % 65521) << 16) | (calOneStr(nonce, c) % 65521)
	k := encNum(j >> 2)
	l := encNum((j << 28) | ((524576 ^ uint32(b)) >> 4))
	m := encNum(a)

	n := signHead + d + f + h + string(i) + k + l + m
	o := fmt.Sprintf("%02x", calOneStr3(n, 0)&0xFF)
	return n + o
}
