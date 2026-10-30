package streamfinder

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	biliAPIView    = "https://api.bilibili.com/x/web-interface/view"
	biliAPIPlayURL = "https://api.bilibili.com/x/player/wbi/playurl"
	biliAPISpi     = "https://api.bilibili.com/x/frontend/finger/spi"
	biliAPINav     = "https://api.bilibili.com/x/web-interface/nav"
	biliAPIEpList  = "https://api.bilibili.com/pgc/view/web/ep/list"

	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
)

// BiliVideoFinder resolves regular bilibili videos (BV/av) the same way
// dmlive does: view API for cid/title, then a WBI-signed playurl request
// (DASH), assembled into an mpv EDL with separate audio/video streams.
type BiliVideoFinder struct {
	client *http.Client
	cookie string
}

func NewBiliVideoFinder(cookie string) *BiliVideoFinder {
	return &BiliVideoFinder{
		client: &http.Client{Timeout: 20 * time.Second},
		cookie: cookie,
	}
}

func (f *BiliVideoFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return nil, err
	}

	// Bangumi (ep/ss): resolve the season's episode list first, pick the
	// requested entry, then continue down the ordinary video path with its
	// bvid/cid — the playurl API is the same one regular videos use.
	if isBangumiURL(u) {
		return f.resolveBangumi(ctx, u)
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("invalid bilibili video url")
	}
	id := parts[len(parts)-1]
	if !strings.HasPrefix(id, "BV") && !strings.HasPrefix(id, "av") {
		return nil, fmt.Errorf("not a bilibili video id: %s", id)
	}
	page := 1
	if p := u.Query().Get("p"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	return f.resolveVideo(ctx, id, page)
}

// resolveVideo is the ordinary BV/av path: view API for cid/title, then a
// WBI-signed playurl request (DASH), assembled into an mpv EDL.
func (f *BiliVideoFinder) resolveVideo(ctx context.Context, id string, page int) (*StreamInfo, error) {
	cookie := f.cookie
	if cookie == "" {
		// Guests need a buvid3 fingerprint to pass risk control.
		cookie = f.guestCookie(ctx)
	}

	// 1. view API: cid + title (+ per-page cids)
	viewQS := "bvid=" + url.QueryEscape(id)
	if strings.HasPrefix(id, "av") {
		viewQS = "aid=" + url.QueryEscape(strings.TrimPrefix(id, "av"))
	}
	view, err := f.getJSON(ctx, biliAPIView+"?"+viewQS, cookie)
	if err != nil {
		return nil, err
	}
	if code, _ := view["code"].(float64); code != 0 {
		return nil, fmt.Errorf("bilibili view api rejected %s (code %v)", id, code)
	}
	data, _ := view["data"].(map[string]any)
	if data == nil {
		return nil, errors.New("bilibili view api: no data")
	}
	title, _ := data["title"].(string)
	if bv, ok := data["bvid"].(string); ok && bv != "" {
		id = bv
	}
	cid := ""
	if page == 1 {
		if c := int64(toF(data["cid"])); c > 0 {
			cid = strconv.FormatInt(c, 10)
		}
	} else if pages, ok := data["pages"].([]any); ok {
		for _, p := range pages {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			if int(toF(pm["page"])) == page {
				cid = strconv.FormatInt(int64(toF(pm["cid"])), 10)
				title = fmt.Sprintf("%s · P%d %s", title, page, pm["part"])
				break
			}
		}
	}
	if cid == "" {
		return nil, errors.New("bilibili view api: no cid for page")
	}

	// 2. WBI-signed playurl (DASH)
	imgKey, subKey, err := fetchWBIKeys(ctx, f.client, cookie)
	if err != nil {
		return nil, fmt.Errorf("bilibili wbi keys: %w", err)
	}
	signed := encodeWBI(map[string]string{
		"bvid":  id,
		"cid":   cid,
		"qn":    "0",
		"fnval": "848",
		"fnver": "0",
		"fourk": "1",
	}, imgKey, subKey)
	play, err := f.getJSON(ctx, biliAPIPlayURL+"?"+signed, cookie)
	if err != nil {
		return nil, err
	}
	if code, _ := play["code"].(float64); code != 0 {
		return nil, fmt.Errorf("bilibili playurl rejected (code %v, message %v)", code, play["message"])
	}
	playData, _ := play["data"].(map[string]any)
	if playData == nil {
		return nil, errors.New("bilibili playurl: no data")
	}

	streamURL, err := extractDash(playData)
	if err != nil {
		return nil, err
	}

	return &StreamInfo{
		Title:     title,
		URL:       streamURL,
		Headers:   map[string]string{"User-Agent": userAgent, "Referer": "https://www.bilibili.com/"},
		DanmakuID: cid,
	}, nil
}

// isBangumiURL reports whether u points at a bangumi/play page (ep… or ss…).
func isBangumiURL(u *url.URL) bool {
	if !strings.Contains(u.Path, "bangumi/play/") {
		return false
	}
	seg := u.Path[strings.LastIndex(u.Path, "/")+1:]
	return strings.HasPrefix(seg, "ep") || strings.HasPrefix(seg, "ss")
}

// resolveBangumi turns an ep/ss page into a playable stream. The season's
// episode list comes from the pgc API; the entry is chosen by the ep id in
// the URL, by ?p=, or defaults to the first episode.
func (f *BiliVideoFinder) resolveBangumi(ctx context.Context, u *url.URL) (*StreamInfo, error) {
	seg := u.Path[strings.LastIndex(u.Path, "/")+1:]

	query := "season_id=" + url.QueryEscape(strings.TrimPrefix(seg, "ss"))
	if strings.HasPrefix(seg, "ep") {
		query = "ep_id=" + url.QueryEscape(strings.TrimPrefix(seg, "ep"))
	}
	list, err := f.getJSON(ctx, biliAPIEpList+"?"+query, f.cookie)
	if err != nil {
		return nil, err
	}
	if code, _ := list["code"].(float64); code != 0 {
		return nil, fmt.Errorf("bilibili ep list rejected (code %v, message %v)", code, list["message"])
	}
	result, _ := list["result"].(map[string]any)
	if result == nil {
		return nil, errors.New("bilibili ep list: no result")
	}
	eps, _ := result["episodes"].([]any)
	if len(eps) == 0 {
		return nil, errors.New("bilibili ep list: no episodes")
	}

	// Pick the episode: an explicit ep id wins; otherwise ?p= (1-based);
	// an out-of-range p clamps to the last episode, as dmlive does.
	want := -1
	if strings.HasPrefix(seg, "ep") {
		for i, e := range eps {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			if link, _ := em["link"].(string); strings.Contains(link, seg) {
				want = i
				break
			}
		}
		if want < 0 {
			return nil, fmt.Errorf("episode %s not found in season list", seg)
		}
	} else {
		want = 0
		if p := u.Query().Get("p"); p != "" {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				want = v - 1
			}
		}
		if want >= len(eps) {
			want = len(eps) - 1
		}
	}

	ep, _ := eps[want].(map[string]any)
	if ep == nil {
		return nil, errors.New("bilibili ep list: malformed episode entry")
	}
	bvid, _ := ep["bvid"].(string)
	cid := strconv.FormatInt(int64(toF(ep["cid"])), 10)
	if bvid == "" || cid == "" || cid == "0" {
		return nil, errors.New("bilibili ep list: episode has no bvid/cid")
	}
	title, _ := ep["share_copy"].(string)
	if title == "" {
		title, _ = ep["title"].(string)
	}
	title = fmt.Sprintf("%s · P%d", title, want+1)

	info, err := f.resolveVideo(ctx, bvid, 1)
	if err != nil {
		return nil, err
	}
	info.Title = title
	// The ep list already gave us the cid; keep it authoritative so the
	// danmaku track matches the episode, not the bvid's first page.
	info.DanmakuID = cid
	return info, nil
}

// extractDash picks the highest-bandwidth video+audio DASH streams and builds
// the same mpv EDL dmlive uses; falls back to durl (progressive) if present.
func extractDash(playData map[string]any) (string, error) {
	dash, _ := playData["dash"].(map[string]any)
	if dash != nil {
		videoURL := bestStreamURL(dash["video"])
		audioURL := bestStreamURL(dash["audio"])
		if videoURL != "" && audioURL != "" {
			return edlTwoStream(audioURL, videoURL), nil
		}
		if videoURL != "" {
			return videoURL, nil
		}
	}
	if durls, ok := playData["d"].([]any); ok && len(durls) > 0 {
		if d0, ok := durls[0].(map[string]any); ok {
			if u, _ := d0["url"].(string); u != "" {
				return u, nil
			}
		}
	}
	return "", errors.New("bilibili playurl: no playable stream")
}

func bestStreamURL(v any) string {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	type cand struct {
		bw float64
		ul string
	}
	var cands []cand
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		base, _ := m["baseUrl"].(string)
		if base == "" {
			base, _ = m["base_url"].(string)
		}
		if base == "" {
			continue
		}
		cands = append(cands, cand{bw: toF(m["bandwidth"]), ul: base})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].bw > cands[j].bw })
	return cands[0].ul
}

// edlTwoStream mirrors dmlive's reload_edl_video EDL: audio first, then video,
// each embedded as %len%url (mpv EDL quoted-parameter syntax: the parameter
// must start with %, then byte count, then %, then that many bytes verbatim —
// this protects the '=', ',' and ';' inside the signed CDN URLs).
func edlTwoStream(urlA, urlV string) string {
	return fmt.Sprintf(
		"edl://!no_clip;!no_chapters;%%%d%%%s;!new_stream;!no_clip;!no_chapters;%%%d%%%s",
		len([]rune(urlA)), urlA, len([]rune(urlV)), urlV)
}

// guestCookie acquires a buvid3 fingerprint for cookieless requests.
func (f *BiliVideoFinder) guestCookie(ctx context.Context) string {
	spi, err := f.getJSON(ctx, biliAPISpi, "")
	if err != nil {
		return ""
	}
	data, _ := spi["data"].(map[string]any)
	if data == nil {
		return ""
	}
	b3, _ := data["b_3"].(string)
	if b3 == "" {
		return ""
	}
	b4, _ := data["b_4"].(string)
	bnut := strconv.FormatInt(time.Now().Unix(), 10)
	return fmt.Sprintf("buvid3=%s; b_nut=%s; buvid4=%s", b3, bnut, b4)
}

func (f *BiliVideoFinder) getJSON(ctx context.Context, api, cookie string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("bilibili api %s: bad json: %.120s", api, body)
	}
	return m, nil
}

func toF(v any) float64 {
	f, _ := v.(float64)
	return f
}

// --- WBI signing, mirrored from internal/engine/danmaku/bilibili.go ---
// (kept local so streamfinder does not depend on the danmaku package)

var wbiMixinKeyEncTab = [64]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61,
	26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20,
	34, 44, 52,
}

func fetchWBIKeys(ctx context.Context, client *http.Client, cookie string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", biliAPINav, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var nav struct {
		Data struct {
			WbiImg struct {
				ImgURL string `json:"img_url"`
				SubURL string `json:"sub_url"`
			} `json:"wbi_img"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&nav); err != nil {
		return "", "", err
	}
	return wbiTakeFilename(nav.Data.WbiImg.ImgURL), wbiTakeFilename(nav.Data.WbiImg.SubURL), nil
}

func wbiTakeFilename(u string) string {
	s := u
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[:i]
	}
	return s
}

func wbiMixinKey(orig string) string {
	out := make([]byte, 0, 32)
	for i := 0; i < 32; i++ {
		out = append(out, orig[wbiMixinKeyEncTab[i]])
	}
	return string(out)
}

func wbiURLEncode(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-_.~", c) {
			b.WriteRune(c)
		} else if strings.ContainsRune("!'()*", c) {
			// filtered per reference implementation
		} else {
			for _, by := range []byte(string(c)) {
				fmt.Fprintf(&b, "%%%02X", by)
			}
		}
	}
	return b.String()
}

func encodeWBI(params map[string]string, imgKey, subKey string) string {
	mixinKey := wbiMixinKey(imgKey + subKey)
	params["wts"] = strconv.FormatInt(time.Now().Unix(), 10)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(params))
	for _, k := range keys {
		parts = append(parts, wbiURLEncode(k)+"="+wbiURLEncode(params[k]))
	}
	query := strings.Join(parts, "&")
	sum := md5.Sum([]byte(query + mixinKey))
	return query + "&w_rid=" + hex.EncodeToString(sum[:])
}
