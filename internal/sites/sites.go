package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"revda/internal/engine/streamfinder"
)

type RoomInfo struct {
	RoomCode string `json:"room_code"`
	Cover    string `json:"cover"`
	Title    string `json:"title"`
	Owner    string `json:"owner"`
	IsLiving bool   `json:"is_living"`
}

type Service struct {
	client    *http.Client
	semChina  chan struct{} // for bilibili, douyu, huya
	semGlobal chan struct{} // for youtube, twitch
}

func NewService() *Service {
	transport := &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     60 * time.Second,
	}
	return &Service{
		client: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
		semChina:  make(chan struct{}, 4),
		semGlobal: make(chan struct{}, 4),
	}
}

const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// NormalizeInput parses user input (can be room code like bi-123 or full URL) into standard roomCode and playURL
func NormalizeInput(input string) (roomCode string, playURL string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errors.New("empty room input")
	}

	// 1. Direct code prefix
	if strings.HasPrefix(input, "bi-") {
		rid := strings.TrimPrefix(input, "bi-")
		return input, "https://live.bilibili.com/" + rid, nil
	}
	if strings.HasPrefix(input, "do-") {
		rid := strings.TrimPrefix(input, "do-")
		return input, "https://www.douyu.com/" + rid, nil
	}
	if strings.HasPrefix(input, "hu-") {
		rid := strings.TrimPrefix(input, "hu-")
		return input, "https://www.huya.com/" + rid, nil
	}
	if strings.HasPrefix(input, "yt-") {
		rid := strings.TrimPrefix(input, "yt-")
		return input, "https://www.youtube.com/@" + rid, nil
	}
	if strings.HasPrefix(input, "ytv-") {
		rid := strings.TrimPrefix(input, "ytv-")
		return input, "https://www.youtube.com/watch?v=" + rid, nil
	}
	if strings.HasPrefix(input, "tw-") {
		rid := strings.TrimPrefix(input, "tw-")
		return input, "https://www.twitch.tv/" + rid, nil
	}
	if strings.HasPrefix(input, "dy-") {
		rid := strings.TrimPrefix(input, "dy-")
		return input, "https://live.douyin.com/" + rid, nil
	}
	if strings.HasPrefix(input, "BV") || strings.HasPrefix(input, "av") {
		parts := strings.Split(input, ":")
		if len(parts) == 2 {
			return input, fmt.Sprintf("https://www.bilibili.com/video/%s?p=%s", parts[0], parts[1]), nil
		}
		return input, "https://www.bilibili.com/video/" + input, nil
	}
	if strings.HasPrefix(input, "ep") || strings.HasPrefix(input, "ss") {
		parts := strings.Split(input, ":")
		if len(parts) == 2 {
			return input, fmt.Sprintf("https://www.bilibili.com/bangumi/play/%s?p=%s", parts[0], parts[1]), nil
		}
		return input, "https://www.bilibili.com/bangumi/play/" + input, nil
	}

	// 2. URL parsing
	if strings.Contains(input, "live.bilibili.com/") {
		re := regexp.MustCompile(`live\.bilibili\.com/(\d+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "bi-" + m[1], "https://live.bilibili.com/" + m[1], nil
		}
	}
	if strings.Contains(input, "douyu.com/") {
		re := regexp.MustCompile(`douyu\.com/(\w+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "do-" + m[1], "https://www.douyu.com/" + m[1], nil
		}
	}
	if strings.Contains(input, "huya.com/") {
		re := regexp.MustCompile(`huya\.com/(\w+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "hu-" + m[1], "https://www.huya.com/" + m[1], nil
		}
	}
	if strings.Contains(input, "twitch.tv/") {
		re := regexp.MustCompile(`twitch\.tv/([a-zA-Z0-9_]+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "tw-" + m[1], "https://www.twitch.tv/" + m[1], nil
		}
	}
	// live.douyin.com/{web_rid}; v.douyin.com short links need a redirect
	// follow, which NormalizeInput deliberately does not do.
	if strings.Contains(input, "live.douyin.com/") {
		re := regexp.MustCompile(`live\.douyin\.com/(\d+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "dy-" + m[1], "https://live.douyin.com/" + m[1], nil
		}
	}
	if strings.Contains(input, "youtube.com/watch") {
		re := regexp.MustCompile(`[?&]v=([a-zA-Z0-9_-]+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "ytv-" + m[1], "https://www.youtube.com/watch?v=" + m[1], nil
		}
	}
	if strings.Contains(input, "youtube.com/@") {
		re := regexp.MustCompile(`youtube\.com/@([a-zA-Z0-9_.-]+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return "yt-" + m[1], "https://www.youtube.com/@" + m[1], nil
		}
	}
	if strings.Contains(input, "bilibili.com/video/") {
		re := regexp.MustCompile(`bilibili\.com/video/(BV[a-zA-Z0-9]+|av\d+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			return m[1], "https://www.bilibili.com/video/" + m[1], nil
		}
	}
	if strings.Contains(input, "bilibili.com/bangumi/play/") {
		re := regexp.MustCompile(`bangumi/play/(ep\d+|ss\d+)`)
		if m := re.FindStringSubmatch(input); len(m) > 1 {
			seg := m[1]
			// A ?p= on the URL picks the episode for ss… codes.
			if p := regexp.MustCompile(`[?&]p=(\d+)`).FindStringSubmatch(input); p != nil &&
				strings.HasPrefix(seg, "ss") {
				return seg + ":" + p[1], fmt.Sprintf("https://www.bilibili.com/bangumi/play/%s?p=%s", seg, p[1]), nil
			}
			return seg, "https://www.bilibili.com/bangumi/play/" + seg, nil
		}
	}

	return "", "", fmt.Errorf("unknown room code or URL: %s", input)
}

func (s *Service) GetRoomInfo(ctx context.Context, roomCode string) (*RoomInfo, error) {
	if strings.HasPrefix(roomCode, "bi-") {
		select {
		case s.semChina <- struct{}{}:
			defer func() { <-s.semChina }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getBilibiliRoomInfo(ctx, strings.TrimPrefix(roomCode, "bi-"))
	}
	if strings.HasPrefix(roomCode, "do-") {
		select {
		case s.semChina <- struct{}{}:
			defer func() { <-s.semChina }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getDouyuRoomInfo(ctx, strings.TrimPrefix(roomCode, "do-"))
	}
	if strings.HasPrefix(roomCode, "hu-") {
		select {
		case s.semChina <- struct{}{}:
			defer func() { <-s.semChina }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getHuyaRoomInfo(ctx, strings.TrimPrefix(roomCode, "hu-"))
	}
	if strings.HasPrefix(roomCode, "yt-") {
		select {
		case s.semGlobal <- struct{}{}:
			defer func() { <-s.semGlobal }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getYoutubeRoomInfo(ctx, strings.TrimPrefix(roomCode, "yt-"), false)
	}
	if strings.HasPrefix(roomCode, "ytv-") {
		select {
		case s.semGlobal <- struct{}{}:
			defer func() { <-s.semGlobal }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getYoutubeRoomInfo(ctx, strings.TrimPrefix(roomCode, "ytv-"), true)
	}
	if strings.HasPrefix(roomCode, "tw-") {
		select {
		case s.semGlobal <- struct{}{}:
			defer func() { <-s.semGlobal }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getTwitchRoomInfo(ctx, strings.TrimPrefix(roomCode, "tw-"))
	}
	if strings.HasPrefix(roomCode, "dy-") {
		select {
		case s.semChina <- struct{}{}:
			defer func() { <-s.semChina }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getDouyinRoomInfo(ctx, strings.TrimPrefix(roomCode, "dy-"))
	}
	if strings.HasPrefix(roomCode, "BV") || strings.HasPrefix(roomCode, "av") {
		select {
		case s.semChina <- struct{}{}:
			defer func() { <-s.semChina }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getBilibiliVideoInfo(ctx, roomCode)
	}
	if strings.HasPrefix(roomCode, "ep") || strings.HasPrefix(roomCode, "ss") {
		select {
		case s.semChina <- struct{}{}:
			defer func() { <-s.semChina }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.getBangumiInfo(ctx, roomCode)
	}

	return nil, fmt.Errorf("unsupported room code: %s", roomCode)
}

func (s *Service) getBilibiliRoomInfo(ctx context.Context, rid string) (*RoomInfo, error) {
	// Try official web-room base info API first
	apiURL := fmt.Sprintf("https://api.live.bilibili.com/xlive/web-room/v1/index/getRoomBaseInfo?room_ids=%s&req_biz=web_room_componet", rid)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)

	resp, err := s.client.Do(req)
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var result struct {
			Code int `json:"code"`
			Data struct {
				ByRoomIds map[string]struct {
					Title      string `json:"title"`
					Uname      string `json:"uname"`
					Cover      string `json:"cover"`
					Background string `json:"background"`
					LiveStatus int    `json:"live_status"`
				} `json:"by_room_ids"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &result) == nil && result.Code == 0 {
			if item, ok := result.Data.ByRoomIds[rid]; ok {
				cover := item.Cover
				if cover == "" {
					cover = item.Background
				}
				return &RoomInfo{
					RoomCode: "bi-" + rid,
					Cover:    cover,
					Title:    item.Title,
					Owner:    item.Uname,
					IsLiving: item.LiveStatus == 1,
				}, nil
			}
		}
	}

	// Fallback to room HTML page scraping
	pageURL := "https://live.bilibili.com/" + rid
	req2, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("User-Agent", defaultUA)
	resp2, err := s.client.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	bodyStr := string(body2)

	re := regexp.MustCompile(`window\.__NEPTUNE_IS_MY_WAIFU__\s*=\s*(\{.+?\})\s*</script>`)
	if m := re.FindStringSubmatch(bodyStr); len(m) > 1 {
		var j map[string]any
		if json.Unmarshal([]byte(m[1]), &j) == nil {
			info := &RoomInfo{RoomCode: "bi-" + rid}
			if rInfo, ok := extractNestedMap(j, "roomInfoRes", "data", "room_info"); ok {
				if title, ok := rInfo["title"].(string); ok {
					info.Title = title
				}
				if cover, ok := rInfo["cover"].(string); ok {
					info.Cover = cover
				}
				if kf, ok := rInfo["keyframe"].(string); ok && kf != "" {
					info.Cover = kf
				}
				if ls, ok := rInfo["live_status"].(float64); ok {
					info.IsLiving = ls == 1
				}
			}
			if aInfo, ok := extractNestedMap(j, "roomInfoRes", "data", "anchor_info", "base_info"); ok {
				if uname, ok := aInfo["uname"].(string); ok {
					info.Owner = uname
				}
			}
			if info.Title != "" {
				return info, nil
			}
		}
	}

	return nil, fmt.Errorf("bilibili room %s not found or parsing failed", rid)
}

func (s *Service) getDouyuRoomInfo(ctx context.Context, rid string) (*RoomInfo, error) {
	apiURL := "https://www.douyu.com/betard/" + rid
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Referer", "https://www.douyu.com/"+rid)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Room struct {
			RoomPic    string `json:"room_pic"`
			RoomName   string `json:"room_name"`
			Nickname   string `json:"nickname"`
			ShowStatus int    `json:"show_status"`
			VideoLoop  int    `json:"videoLoop"`
		} `json:"room"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("douyu parse error: %w", err)
	}

	return &RoomInfo{
		RoomCode: "do-" + rid,
		Cover:    result.Room.RoomPic,
		Title:    result.Room.RoomName,
		Owner:    result.Room.Nickname,
		IsLiving: result.Room.ShowStatus == 1 && result.Room.VideoLoop == 0,
	}, nil
}

func (s *Service) getHuyaRoomInfo(ctx context.Context, rid string) (*RoomInfo, error) {
	pageURL := "https://www.huya.com/" + rid
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	reProfile := regexp.MustCompile(`var\s+TT_PROFILE_INFO\s+=\s+({.+?});`)
	reRoom := regexp.MustCompile(`var\s+TT_ROOM_DATA\s+=\s+({.+?});`)

	mP := reProfile.FindStringSubmatch(bodyStr)
	mR := reRoom.FindStringSubmatch(bodyStr)
	if len(mP) < 2 || len(mR) < 2 {
		return nil, fmt.Errorf("huya room %s html parsing failed", rid)
	}

	var pData struct {
		Nick   string `json:"nick"`
		Avatar string `json:"avatar"`
	}
	var rData struct {
		Introduction string `json:"introduction"`
		Screenshot   string `json:"screenshot"`
		IsOn         bool   `json:"isOn"`
	}
	_ = json.Unmarshal([]byte(mP[1]), &pData)
	_ = json.Unmarshal([]byte(mR[1]), &rData)

	cover := rData.Screenshot
	if !rData.IsOn || cover == "" {
		cover = pData.Avatar
	}
	title := rData.Introduction
	if title == "" {
		title = "没有直播标题"
	}

	return &RoomInfo{
		RoomCode: "hu-" + rid,
		Cover:    cover,
		Title:    title,
		Owner:    pData.Nick,
		IsLiving: rData.IsOn,
	}, nil
}

func (s *Service) getYoutubeRoomInfo(ctx context.Context, rid string, isVideo bool) (*RoomInfo, error) {
	targetURL := fmt.Sprintf("https://www.youtube.com/@%s/live", rid)
	if isVideo {
		targetURL = fmt.Sprintf("https://www.youtube.com/watch?v=%s", rid)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	rePlayer := regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*({.+?});`)
	if m := rePlayer.FindStringSubmatch(bodyStr); len(m) > 1 {
		var j struct {
			VideoDetails struct {
				Title     string `json:"title"`
				Author    string `json:"author"`
				IsLive    bool   `json:"isLive"`
				Thumbnail struct {
					Thumbnails []struct {
						URL string `json:"url"`
					} `json:"thumbnails"`
				} `json:"thumbnail"`
			} `json:"videoDetails"`
		}
		if json.Unmarshal([]byte(m[1]), &j) == nil && j.VideoDetails.Title != "" {
			var cover string
			if tns := j.VideoDetails.Thumbnail.Thumbnails; len(tns) > 0 {
				cover = tns[len(tns)-1].URL
			}
			code := "yt-" + rid
			if isVideo {
				code = "ytv-" + rid
			}
			return &RoomInfo{
				RoomCode: code,
				Cover:    cover,
				Title:    j.VideoDetails.Title,
				Owner:    j.VideoDetails.Author,
				IsLiving: j.VideoDetails.IsLive,
			}, nil
		}
	}

	// Fallback to meta tags
	reTitle := regexp.MustCompile(`<meta\s+property="og:title"\s+content="([^"]+)"`)
	reImage := regexp.MustCompile(`<meta\s+property="og:image"\s+content="([^"]+)"`)
	title := ""
	if m := reTitle.FindStringSubmatch(bodyStr); len(m) > 1 {
		title = m[1]
	}
	cover := ""
	if m := reImage.FindStringSubmatch(bodyStr); len(m) > 1 {
		cover = m[1]
	}

	code := "yt-" + rid
	if isVideo {
		code = "ytv-" + rid
	}
	return &RoomInfo{
		RoomCode: code,
		Cover:    cover,
		Title:    title,
		Owner:    rid,
		IsLiving: false,
	}, nil
}

func (s *Service) getTwitchRoomInfo(ctx context.Context, rid string) (*RoomInfo, error) {
	pageURL := "https://www.twitch.tv/" + rid
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	reLD := regexp.MustCompile(`<script\s+type="application/ld\+json">({.+?})</script>`)
	if m := reLD.FindStringSubmatch(bodyStr); len(m) > 1 {
		var j map[string]any
		if json.Unmarshal([]byte(m[1]), &j) == nil {
			if graph, ok := j["@graph"].([]any); ok && len(graph) > 0 {
				if item, ok := graph[0].(map[string]any); ok {
					title, _ := item["description"].(string)
					owner, _ := item["name"].(string)
					cover := ""
					if thumbs, ok := item["thumbnailUrl"].([]any); ok && len(thumbs) > 0 {
						cover, _ = thumbs[len(thumbs)-1].(string)
					}
					return &RoomInfo{
						RoomCode: "tw-" + rid,
						Cover:    cover,
						Title:    title,
						Owner:    owner,
						IsLiving: true,
					}, nil
				}
			}
		}
	}

	// Fallback to meta tags
	reTitle := regexp.MustCompile(`<meta\s+(?:property="og:title"|content="[^"]*"\s+property="og:title")\s+content="([^"]*)"`)
	reImage := regexp.MustCompile(`<meta\s+(?:property="og:image"|content="[^"]*"\s+property="og:image")\s+content="([^"]*)"`)
	owner := rid
	if m := reTitle.FindStringSubmatch(bodyStr); len(m) > 1 {
		owner = strings.TrimSuffix(m[1], " - Twitch")
	}
	cover := ""
	if m := reImage.FindStringSubmatch(bodyStr); len(m) > 1 {
		cover = m[1]
	}

	return &RoomInfo{
		RoomCode: "tw-" + rid,
		Cover:    cover,
		Title:    "没有直播标题",
		Owner:    owner,
		IsLiving: false,
	}, nil
}

func (s *Service) getBilibiliVideoInfo(ctx context.Context, vid string) (*RoomInfo, error) {
	bvid := strings.Split(vid, ":")[0]
	apiURL := "https://api.bilibili.com/x/web-interface/view?bvid=" + bvid
	if strings.HasPrefix(bvid, "av") {
		apiURL = "https://api.bilibili.com/x/web-interface/view?aid=" + strings.TrimPrefix(bvid, "av")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Code int `json:"code"`
		Data struct {
			Title string `json:"title"`
			Pic   string `json:"pic"`
			Owner struct {
				Name string `json:"name"`
			} `json:"owner"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Code != 0 {
		return nil, fmt.Errorf("bilibili video parse error: %v", err)
	}

	return &RoomInfo{
		RoomCode: vid,
		Cover:    result.Data.Pic,
		Title:    result.Data.Title,
		Owner:    result.Data.Owner.Name,
		IsLiving: false,
	}, nil
}

// getBangumiInfo resolves an ep/ss code (with an optional ":n" episode index)
// through the pgc episode list, mirroring what the player's finder does.
func (s *Service) getBangumiInfo(ctx context.Context, code string) (*RoomInfo, error) {
	seg := code
	if i := strings.Index(code, ":"); i >= 0 {
		seg = code[:i]
	}

	apiURL := "https://api.bilibili.com/pgc/view/web/ep/list"
	if strings.HasPrefix(seg, "ep") {
		apiURL += "?ep_id=" + strings.TrimPrefix(seg, "ep")
	} else {
		apiURL += "?season_id=" + strings.TrimPrefix(seg, "ss")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data struct {
			Episodes []struct {
				Bvid      string `json:"bvid"`
				Cid       int64  `json:"cid"`
				Title     string `json:"title"`
				ShareCopy string `json:"share_copy"`
				Cover     string `json:"cover"`
				Link      string `json:"link"`
			} `json:"episodes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Code != 0 {
		return nil, fmt.Errorf("bilibili bangumi parse error: code %d %s", result.Code, result.Msg)
	}
	eps := result.Data.Episodes
	if len(eps) == 0 {
		return nil, errors.New("bilibili bangumi: no episodes")
	}

	idx := 0
	if strings.HasPrefix(seg, "ep") {
		for i, e := range eps {
			if strings.Contains(e.Link, seg) {
				idx = i
				break
			}
		}
	} else if i := strings.Index(code, ":"); i >= 0 {
		if v, err := strconv.Atoi(code[i+1:]); err == nil && v > 0 {
			idx = v - 1
		}
	}
	if idx >= len(eps) {
		idx = len(eps) - 1
	}
	ep := eps[idx]

	title := ep.ShareCopy
	if title == "" {
		title = ep.Title
	}
	return &RoomInfo{
		RoomCode: code,
		Cover:    ep.Cover,
		Title:    fmt.Sprintf("%s · P%d", title, idx+1),
		Owner:    "番剧",
		IsLiving: false,
	}, nil
}

// getDouyinRoomInfo delegates to the streamfinder package: the enter API needs
// the a_bogus signature, which lives there (and is embedded once).
func (s *Service) getDouyinRoomInfo(ctx context.Context, rid string) (*RoomInfo, error) {
	meta, err := streamfinder.NewDouyinFinder().RoomMeta(ctx, rid)
	if err != nil {
		return nil, err
	}
	return &RoomInfo{
		RoomCode: "dy-" + rid,
		Cover:    meta.Cover,
		Title:    meta.Title,
		Owner:    meta.Owner,
		IsLiving: meta.IsLiving,
	}, nil
}

func extractNestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	curr := m
	for _, k := range keys {
		next, ok := curr[k].(map[string]any)
		if !ok {
			return nil, false
		}
		curr = next
	}
	return curr, true
}
