package danmaku

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"golang.org/x/net/websocket"
)

//go:embed douyin_sign.js
var douyinSignJS string

const (
	douyinHomeURL    = "https://live.douyin.com/"
	douyinWSURL      = "wss://webcast100-ws-web-lq.douyin.com/webcast/im/push/v2/"
	douyinWSSReferer = "https://live.douyin.com/"

	douyinUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	// Fixed anonymous device id used by DouyinLiveWebFetcher for the wss
	// endpoint; paired with the ttwid cookie it identifies the "browser".
	douyinWSSDID = "7319483754668557238"
)

type DouyinDanmakuClient struct {
	roomID string // public web rid as in live.douyin.com/{roomID}

	ttwid    string
	internal string // internal numeric room id resolved from the room page

	signOnce sync.Once
	signErr  error
	signFn   goja.Callable
	signVM   *goja.Runtime

	messages chan *DanmakuMessage
}

func NewDouyinDanmakuClient(roomID string) *DouyinDanmakuClient {
	return &DouyinDanmakuClient{
		roomID:   roomID,
		messages: make(chan *DanmakuMessage, 200),
	}
}

func (c *DouyinDanmakuClient) Messages() <-chan *DanmakuMessage {
	return c.messages
}

func (c *DouyinDanmakuClient) Start(ctx context.Context) error {
	backoff := 3 * time.Second
	for {
		c.runOnce(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff += 3 * time.Second
		}
	}
}

var douyinRoomIDRes = []*regexp.Regexp{
	regexp.MustCompile(`roomId\\":\\"(\d+)\\"`),
	regexp.MustCompile(`roomId":"(\d+)`),
	regexp.MustCompile(`roomId": ?(\d+)`),
}

// resolve fetches the anonymous ttwid cookie and maps the public web rid to
// the internal room id the wss endpoint expects.
func (c *DouyinDanmakuClient) resolve(ctx context.Context) error {
	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", douyinHomeURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", douyinUA)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "ttwid" {
			c.ttwid = ck.Value
		}
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if c.ttwid == "" {
		return errors.New("douyin: no ttwid cookie issued")
	}

	pageReq, err := http.NewRequestWithContext(ctx, "GET", douyinHomeURL+c.roomID, nil)
	if err != nil {
		return err
	}
	pageReq.Header.Set("User-Agent", douyinUA)
	pageReq.Header.Set("Referer", douyinHomeURL)
	pageReq.Header.Set("Cookie", "ttwid="+c.ttwid)
	pageResp, err := client.Do(pageReq)
	if err != nil {
		return err
	}
	defer pageResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(pageResp.Body, 8<<20))
	if err != nil {
		return err
	}
	for _, re := range douyinRoomIDRes {
		if m := re.FindStringSubmatch(string(body)); len(m) > 1 {
			c.internal = m[1]
			break
		}
	}
	if c.internal == "" {
		return fmt.Errorf("douyin: no roomId found on page %s", c.roomID)
	}
	return nil
}

// initSign loads douyin_sign.js (the webmssdk bundle) into goja once; it
// self-bootstraps document/window/navigator shims.
func (c *DouyinDanmakuClient) initSign() error {
	c.signOnce.Do(func() {
		vm := goja.New()
		if _, err := vm.RunString(douyinSignJS); err != nil {
			c.signErr = fmt.Errorf("douyin sign js init: %w", err)
			return
		}
		fn, ok := goja.AssertFunction(vm.Get("get_sign"))
		if !ok {
			c.signErr = errors.New("douyin sign js: get_sign missing")
			return
		}
		c.signVM, c.signFn = vm, fn
	})
	return c.signErr
}

func (c *DouyinDanmakuClient) wssSignature(query string) (string, error) {
	if err := c.initSign(); err != nil {
		return "", err
	}
	// The signature covers these 13 params as k=v pairs joined by ','
	// (values percent-encoded exactly as they appear in the wss URL).
	keys := []string{
		"live_id", "aid", "version_code", "webcast_sdk_version",
		"room_id", "sub_room_id", "sub_channel_id", "did_rule",
		"user_unique_id", "device_platform", "device_type", "ac",
		"identity",
	}
	values := map[string]string{}
	for _, pair := range strings.Split(query, "&") {
		if k, v, ok := strings.Cut(pair, "="); ok {
			values[k] = v
		}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + values[k]
	}
	sum := md5.Sum([]byte(strings.Join(parts, ",")))

	v, err := c.signFn(goja.Undefined(), c.signVM.ToValue(hex.EncodeToString(sum[:])))
	if err != nil {
		return "", fmt.Errorf("douyin sign calc: %w", err)
	}
	if s := v.String(); s != "" {
		return s, nil
	}
	return "", errors.New("douyin sign empty")
}

// douyinQueryEscape matches python urlencode: '+' for space is avoided so the
// wire format stays identical to the reference implementation (%20).
func douyinQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func (c *DouyinDanmakuClient) buildWSS() (string, error) {
	pairs := []string{
		"app_name=douyin_web",
		"version_code=180800",
		"webcast_sdk_version=1.0.14-beta.0",
		"update_version_code=1.0.14-beta.0",
		"compress=gzip",
		"device_platform=web",
		"cookie_enabled=true",
		"screen_width=1536",
		"screen_height=864",
		"browser_language=zh-CN",
		"browser_platform=Win32",
		"browser_name=Mozilla",
		"browser_version=" + douyinQueryEscape("5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"),
		"browser_online=true",
		"tz_name=Asia/Shanghai",
		"cursor=d-1_u-1_fh-7392091211001140287_t-1721106114633_r-1",
		"internal_ext=" + douyinQueryEscape(fmt.Sprintf(
			"internal_src:dim|wss_push_room_id:%s|wss_push_did:%s|first_req_ms:1721106114541|fetch_time:1721106114633|seq:1|wss_info:0-1721106114633-0-0|wrds_v:7392094459690748497",
			c.internal, douyinWSSDID)),
		"host=https%3A%2F%2Flive.douyin.com",
		"aid=6383",
		"live_id=1",
		"did_rule=3",
		"endpoint=live_pc",
		"support_wrds=1",
		"user_unique_id=" + douyinWSSDID,
		"im_path=%2Fwebcast%2Fim%2Ffetch%2F",
		"identity=audience",
		"need_persist_msg_count=15",
		"insert_task_id=",
		"live_reason=",
		"room_id=" + c.internal,
		"heartbeatDuration=0",
	}
	query := strings.Join(pairs, "&")

	sig, err := c.wssSignature(query)
	if err != nil {
		return "", err
	}
	return douyinWSURL + "?" + query + "&signature=" + sig, nil
}

func (c *DouyinDanmakuClient) runOnce(ctx context.Context) error {
	if c.ttwid == "" || c.internal == "" {
		if err := c.resolve(ctx); err != nil {
			return err
		}
	}
	wssURL, err := c.buildWSS()
	if err != nil {
		return err
	}

	cfg, err := websocket.NewConfig(wssURL, douyinWSSReferer)
	if err != nil {
		return err
	}
	cfg.Header.Set("User-Agent", douyinUA)
	cfg.Header.Set("Cookie", "ttwid="+c.ttwid)

	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return err
	}
	defer ws.Close()
	ws.PayloadType = websocket.BinaryFrame

	var writeMu sync.Mutex
	writeFrame := func(pt byte, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		prev := ws.PayloadType
		ws.PayloadType = pt
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := ws.Write(data)
		ws.PayloadType = prev
		return err
	}

	// Heartbeat every 5s: PushFrame{payloadType:"hb"} sent as a PING frame,
	// mirroring DouyinLiveWebFetcher.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		hb := douyinPushFrame("hb", 0, nil)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if writeFrame(websocket.PingFrame, hb) != nil {
					return
				}
			}
		}
	}()

	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return nil
		}
		ws.SetReadDeadline(time.Now().Add(70 * time.Second))
		n, err := ws.Read(buf)
		if err != nil {
			return err
		}
		c.handleFrame(buf[:n], writeFrame)
	}
}

func (c *DouyinDanmakuClient) handleFrame(raw []byte, writeFrame func(byte, []byte) error) {
	frame, err := pbParse(raw)
	if err != nil {
		return
	}
	payloadType := pbFieldString(pbFirst(frame, 7))
	if payloadType == "hb" { // server heartbeat, nothing to do
		return
	}
	payload := pbFieldBytes(pbFirst(frame, 8))
	if len(payload) == 0 {
		return
	}
	if len(payload) > 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return
		}
		payload, err = io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return
		}
	}

	resp, err := pbParse(payload)
	if err != nil {
		return
	}

	// Ack so the server keeps pushing.
	if pbFieldUint(pbFirst(resp, 9)) > 0 {
		if ext := pbFieldBytes(pbFirst(resp, 5)); len(ext) > 0 {
			var logID uint64
			if lf := pbFirst(frame, 2); lf != nil {
				logID = lf.n
			}
			if ack := douyinPushFrame("ack", logID, ext); len(ack) > 0 {
				writeFrame(websocket.BinaryFrame, ack)
			}
		}
	}

	for _, mf := range pbAll(resp, 1) {
		msg, err := pbParse(mf.val)
		if err != nil {
			continue
		}
		if pbFieldString(pbFirst(msg, 1)) != "WebcastChatMessage" {
			continue
		}
		chat, err := pbParse(pbFieldBytes(pbFirst(msg, 2)))
		if err != nil {
			continue
		}
		content := pbFieldString(pbFirst(chat, 3))
		nick := ""
		if uf := pbFirst(chat, 2); uf != nil {
			if user, err := pbParse(uf.val); err == nil {
				nick = pbFieldString(pbFirst(user, 3)) // User.nickName
			}
		}
		c.emit(nick, content)
	}
}

func (c *DouyinDanmakuClient) emit(nick, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	msg := &DanmakuMessage{
		Nick:     nick,
		Text:     strings.TrimSpace(text),
		Color:    "ffffff",
		Position: 0,
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

// --- minimal protobuf wire decoding (zero deps, like tars.go) ---

type pbField struct {
	num  int
	wire int
	val  []byte // wire types 1/2/5
	n    uint64 // wire type 0 (varint)
}

func pbParse(data []byte) ([]pbField, error) {
	var fields []pbField
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errors.New("protobuf: bad tag")
		}
		data = data[n:]
		f := pbField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("protobuf: bad varint")
			}
			data = data[n:]
			f.n = v
		case 1:
			if len(data) < 8 {
				return nil, errors.New("protobuf: short fixed64")
			}
			f.val, data = data[:8], data[8:]
		case 2:
			l, n := binary.Uvarint(data)
			if n <= 0 || l > uint64(len(data)) {
				return nil, errors.New("protobuf: bad length")
			}
			data = data[n:]
			f.val, data = data[:l], data[l:]
		case 5:
			if len(data) < 4 {
				return nil, errors.New("protobuf: short fixed32")
			}
			f.val, data = data[:4], data[4:]
		default:
			return nil, fmt.Errorf("protobuf: wire type %d", f.wire)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

func pbFirst(fields []pbField, num int) *pbField {
	for i := range fields {
		if fields[i].num == num {
			return &fields[i]
		}
	}
	return nil
}

func pbAll(fields []pbField, num int) []pbField {
	var out []pbField
	for i := range fields {
		if fields[i].num == num {
			out = append(out, fields[i])
		}
	}
	return out
}

func pbFieldBytes(f *pbField) []byte {
	if f == nil {
		return nil
	}
	return f.val
}

func pbFieldString(f *pbField) string {
	if f == nil {
		return ""
	}
	return string(f.val)
}

func pbFieldUint(f *pbField) uint64 {
	if f == nil {
		return 0
	}
	return f.n
}

func pbAppendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

func pbAppendTag(buf []byte, num, wire int) []byte {
	return pbAppendVarint(buf, uint64(num)<<3|uint64(wire))
}

func pbAppendBytes(buf []byte, num int, data []byte) []byte {
	buf = pbAppendTag(buf, num, 2)
	buf = pbAppendVarint(buf, uint64(len(data)))
	return append(buf, data...)
}

// douyinPushFrame encodes PushFrame{logId, payloadType, payload}.
func douyinPushFrame(payloadType string, logID uint64, payload []byte) []byte {
	var buf []byte
	if logID > 0 {
		buf = pbAppendTag(buf, 2, 0)
		buf = pbAppendVarint(buf, logID)
	}
	if payloadType != "" {
		buf = pbAppendBytes(buf, 7, []byte(payloadType))
	}
	if len(payload) > 0 {
		buf = pbAppendBytes(buf, 8, payload)
	}
	return buf
}
