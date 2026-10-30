package danmaku

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

	"golang.org/x/net/websocket"
)

const (
	apiBuvid    = "https://api.bilibili.com/x/frontend/finger/spi"
	apiNav      = "https://api.bilibili.com/x/web-interface/nav"
	apiRoomInit = "https://api.live.bilibili.com/room/v1/Room/room_init"
	apiDMInfo   = "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo"

	biliWSURL   = "wss://broadcastlv.chat.bilibili.com/sub"
	biliReferer = "https://live.bilibili.com/"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

type DanmakuMessage struct {
	Nick     string    `json:"nick"`
	Text     string    `json:"text"`
	Color    string    `json:"color"`
	Position int       `json:"position"` // 0: scroll, 8: centered (superchat)
	Time     time.Time `json:"time"`
}

type BilibiliDanmakuClient struct {
	roomID string
	cookie string

	messages chan *DanmakuMessage
}

func NewBilibiliDanmakuClient(roomID string) *BilibiliDanmakuClient {
	return &BilibiliDanmakuClient{
		roomID:   roomID,
		messages: make(chan *DanmakuMessage, 200),
	}
}

// SetCookie sets an optional login cookie (SESSDATA etc.) to raise quality limits.
func (c *BilibiliDanmakuClient) SetCookie(cookie string) {
	c.cookie = cookie
}

func (c *BilibiliDanmakuClient) Messages() <-chan *DanmakuMessage {
	return c.messages
}

func (c *BilibiliDanmakuClient) Start(ctx context.Context) error {
	for {
		c.runOnce(ctx)
		// Reconnect with backoff; bilibili occasionally drops idle guests.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *BilibiliDanmakuClient) runOnce(ctx context.Context) error {
	auth, err := c.fetchAuth(ctx)
	if err != nil {
		return err
	}

	cfg, err := websocket.NewConfig(biliWSURL, "https://live.bilibili.com")
	if err != nil {
		return err
	}
	cfg.Header.Set("User-Agent", userAgent)
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return err
	}
	defer ws.Close()
	ws.PayloadType = websocket.BinaryFrame // bilibili speaks binary frames

	// AUTH packet (op = 7)
	if _, err := ws.Write(biliPacket(7, auth)); err != nil {
		return err
	}

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go biliHeartbeat(hbCtx, ws)

	buf := make([]byte, 65536)
	var leftover []byte
	for {
		if ctx.Err() != nil {
			return nil
		}
		ws.SetReadDeadline(time.Now().Add(70 * time.Second))
		n, err := ws.Read(buf)
		if err != nil {
			return err
		}
		leftover = append(leftover, buf[:n]...)
		leftover = c.parsePackets(leftover)
	}
}

// fetchAuth walks the same chain the web player uses: buvid fingerprint ->
// real room id -> WBI-signed getDanmuInfo token -> auth JSON.
func (c *BilibiliDanmakuClient) fetchAuth(ctx context.Context) ([]byte, error) {
	hc := &http.Client{Timeout: 10 * time.Second}

	get := func(u string, referer, cookie string) (map[string]any, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("bilibili api %s: bad json: %.120s", u, b)
		}
		return m, nil
	}

	// 1. buvid fingerprint (guests need it to pass risk control)
	spi, err := get(apiBuvid, "", "")
	if err != nil {
		return nil, err
	}
	spiData, _ := spi["data"].(map[string]any)
	buvid3, _ := spiData["b_3"].(string)
	buvid4, _ := spiData["b_4"].(string)
	if buvid3 == "" {
		return nil, errors.New("bilibili: no buvid3 in finger/spi response")
	}
	bnut := strconv.FormatInt(time.Now().Unix(), 10)

	cookie := fmt.Sprintf("buvid3=%s; b_nut=%s; buvid4=%s", buvid3, bnut, buvid4)
	if c.cookie != "" {
		cookie = c.cookie
	}

	// 2. short id -> real room id
	init, err := get(apiRoomInit+"?id="+url.QueryEscape(c.roomID), biliReferer, "")
	if err != nil {
		return nil, err
	}
	initData, _ := init["data"].(map[string]any)
	realID, _ := initData["room_id"].(float64)
	if realID == 0 {
		return nil, fmt.Errorf("bilibili: room_init failed for %s", c.roomID)
	}

	// 3. danmu token via WBI-signed request (unsigned -> code -352 risk control)
	imgKey, subKey, err := fetchWBIKeys(ctx, cookie)
	if err != nil {
		return nil, err
	}
	signed := encodeWBI(map[string]string{"id": strconv.Itoa(int(realID)), "type": "0"}, imgKey, subKey)
	dm, err := get(apiDMInfo+"?"+signed, biliReferer, cookie)
	if err != nil {
		return nil, err
	}
	if code, _ := dm["code"].(float64); code != 0 {
		return nil, fmt.Errorf("bilibili: getDanmuInfo rejected (code %v)", code)
	}
	dmData, _ := dm["data"].(map[string]any)
	token, _ := dmData["token"].(string)

	return json.Marshal(map[string]any{
		"roomid":   int(realID),
		"uid":      0,
		"protover": 2,
		"platform": "web",
		"type":     2,
		"buvid":    buvid3,
		"key":      token,
	})
}

func biliPacket(op uint32, body []byte) []byte {
	p := make([]byte, 16+len(body))
	binaryPutUint32(p[0:4], uint32(16+len(body)))
	binaryPutUint16(p[4:6], 16)
	binaryPutUint16(p[6:8], 1)
	binaryPutUint32(p[8:12], op)
	binaryPutUint32(p[12:16], 1)
	copy(p[16:], body)
	return p
}

// biliHeartbeat sends the "[object Object]" heartbeat every 25s; a missed
// heartbeat gets the guest kicked within ~60s.
func biliHeartbeat(ctx context.Context, ws *websocket.Conn) {
	hb := []byte{
		0x00, 0x00, 0x00, 0x1f, 0x00, 0x10, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x01,
		0x5b, 0x6f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x20, 0x4f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x5d,
	}
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := ws.Write(hb); err != nil {
				return
			}
		}
	}
}

// parsePackets consumes complete packets from data and returns the remainder.
func (c *BilibiliDanmakuClient) parsePackets(data []byte) []byte {
	for len(data) >= 16 {
		totalLen := int(binaryUint32(data[0:4]))
		headerLen := int(binaryUint16(data[4:6]))
		protoVer := binaryUint16(data[6:8])
		op := binaryUint32(data[8:12])

		if totalLen < 16 || totalLen > len(data) {
			break
		}
		if headerLen > totalLen {
			headerLen = 16
		}
		body := data[headerLen:totalLen]

		switch op {
		case 5: // command
			if protoVer == 2 {
				if raw, err := zlibInflate(body); err == nil {
					c.parsePackets(raw)
				}
			} else if protoVer == 0 || protoVer == 1 {
				c.parseSingleJSON(body)
			}
		case 8: // auth reply, nothing to do
		}
		data = data[totalLen:]
	}
	return data
}

func (c *BilibiliDanmakuClient) parseSingleJSON(data []byte) {
	var msg struct {
		Cmd  string `json:"cmd"`
		Info []any  `json:"info"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	switch {
	case strings.HasPrefix(msg.Cmd, "DANMU_MSG") && len(msg.Info) >= 3:
		text, _ := msg.Info[1].(string)
		nick := ""
		if userArr, ok := msg.Info[2].([]any); ok && len(userArr) >= 2 {
			nick, _ = userArr[1].(string)
		}
		c.emit(nick, text, "ffffff", 0)
	case msg.Cmd == "SUPER_CHAT_MESSAGE":
		var sc struct {
			Data struct {
				Message         string `json:"message"`
				BackgroundColor string `json:"background_color_start"`
				UserInfo        struct {
					UName string `json:"uname"`
				} `json:"user_info"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &sc); err == nil && sc.Data.Message != "" {
			color := strings.TrimPrefix(sc.Data.BackgroundColor, "#")
			if color == "" {
				color = "ffffff"
			}
			c.emit(sc.Data.UserInfo.UName, "[SC]"+sc.Data.Message, color, 8)
		}
	}
}

func (c *BilibiliDanmakuClient) emit(nick, text, color string, position int) {
	if strings.TrimSpace(text) == "" {
		return
	}
	msg := &DanmakuMessage{
		Nick:     nick,
		Text:     strings.TrimSpace(text),
		Color:    color,
		Position: position,
		Time:     time.Now(),
	}
	select {
	case c.messages <- msg:
	default: // channel full, drop oldest then retry once
		select {
		case <-c.messages:
		default:
		}
		select {
		case c.messages <- msg:
		default:
		}
	}
}

// --- WBI signing (from SocialSisterYi/bilibili-API-collect, as used by dmlive) ---

var mixinKeyEncTab = [64]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61,
	26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20,
	34, 44, 52,
}

func fetchWBIKeys(ctx context.Context, cookie string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiNav, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
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
	return takeFilename(nav.Data.WbiImg.ImgURL), takeFilename(nav.Data.WbiImg.SubURL), nil
}

func takeFilename(u string) string {
	s := u
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[:i]
	}
	return s
}

func getMixinKey(orig string) string {
	out := make([]byte, 0, 32)
	for i := 0; i < 32; i++ {
		out = append(out, orig[mixinKeyEncTab[i]])
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
	mixinKey := getMixinKey(imgKey + subKey)
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
