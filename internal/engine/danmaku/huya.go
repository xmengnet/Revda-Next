package danmaku

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const (
	huyaWSURL     = "wss://cdnws.api.huya.com"
	huyaReferer   = "https://www.huya.com/"
	huyaMobileUA  = "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Mobile Safari/537.36"
	huyaCmdPush   = 7 // EWSCmdS2C_MsgPushReq
	huyaMsgChat   = 1400
	huyaCmdRegReq = 1 // EWSCmd_RegisterReq
)

// huyaHeartbeat is the fixed heartbeat packet (tWsCmd=3 + tReq struct), byte
// for byte what dmlive sends every 20s to keep the huya session alive.
var huyaHeartbeat = []byte{
	0x00, 0x03, 0x1d, 0x00, 0x00, 0x69, 0x00, 0x00, 0x00, 0x69, 0x10, 0x03, 0x2c, 0x3c, 0x4c, 0x56,
	0x08, 0x6f, 0x6e, 0x6c, 0x69, 0x6e, 0x65, 0x75, 0x69, 0x66, 0x0f, 0x4f, 0x6e, 0x55, 0x73, 0x65,
	0x72, 0x48, 0x65, 0x61, 0x72, 0x74, 0x42, 0x65, 0x61, 0x74, 0x7d, 0x00, 0x00, 0x3c, 0x08, 0x00,
	0x01, 0x06, 0x04, 0x74, 0x52, 0x65, 0x71, 0x1d, 0x00, 0x00, 0x2f, 0x0a, 0x0a, 0x0c, 0x16, 0x00,
	0x26, 0x00, 0x36, 0x07, 0x61, 0x64, 0x72, 0x5f, 0x77, 0x61, 0x70, 0x46, 0x00, 0x0b, 0x12, 0x03,
	0xae, 0xf0, 0x0f, 0x22, 0x03, 0xae, 0xf0, 0x0f, 0x3c, 0x42, 0x6d, 0x52, 0x02, 0x60, 0x5c, 0x60,
	0x01, 0x7c, 0x82, 0x00, 0x0b, 0xb0, 0x1f, 0x9c, 0xac, 0x0b, 0x8c, 0x98, 0x0c, 0xa8, 0x0c,
}

type HuyaDanmakuClient struct {
	roomID string

	messages chan *DanmakuMessage
}

func NewHuyaDanmakuClient(roomID string) *HuyaDanmakuClient {
	return &HuyaDanmakuClient{
		roomID:   roomID,
		messages: make(chan *DanmakuMessage, 200),
	}
}

func (c *HuyaDanmakuClient) Messages() <-chan *DanmakuMessage {
	return c.messages
}

func (c *HuyaDanmakuClient) Start(ctx context.Context) error {
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

func (c *HuyaDanmakuClient) runOnce(ctx context.Context) error {
	ayyuid, topsid, subsid, err := c.fetchRoomIDs(ctx)
	if err != nil {
		return err
	}

	cfg, err := websocket.NewConfig(huyaWSURL, huyaReferer)
	if err != nil {
		return err
	}
	cfg.Header.Set("User-Agent", userAgent)
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return err
	}
	defer ws.Close()
	ws.PayloadType = websocket.BinaryFrame

	if _, err := ws.Write(buildHuyaRegister(ayyuid, topsid, subsid)); err != nil {
		return err
	}

	// The read has no deadline (quiet rooms send nothing); ctx cancellation
	// and heartbeat write failures close the conn to unblock it.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		<-hbCtx.Done()
		ws.Close()
	}()
	go huyaHeartbeatLoop(hbCtx, ws)

	buf := make([]byte, 1<<18)
	for {
		n, err := ws.Read(buf) // one websocket message per read
		if err != nil {
			return err
		}
		if msg := c.decodeMsg(buf[:n]); msg != nil {
			c.emit(msg)
		}
	}
}

// fetchRoomIDs fetches the mobile room page and extracts the ids the danmaku
// routing needs: ayyuid (streamer uid), TOPSID and SUBSID (sub-session ids).
func (c *HuyaDanmakuClient) fetchRoomIDs(ctx context.Context) (ayyuid, topsid, subsid int64, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://m.huya.com/"+c.roomID, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("User-Agent", huyaMobileUA)
	req.Header.Set("Referer", huyaReferer)

	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, 0, err
	}
	return extractHuyaIDs(string(body))
}

var (
	// The first lYyid on the mobile page is the streamer's yyid
	// (tProfileInfo); lChannelId/lSubChannelId carry the danmaku routing ids.
	reHuyaAyyuid = regexp.MustCompile(`lYyid":(\d+)`)
	reHuyaTopsid = regexp.MustCompile(`lChannelId":(\d+)`)
	reHuyaSubsid = regexp.MustCompile(`lSubChannelId":(\d+)`)
)

func extractHuyaIDs(page string) (ayyuid, topsid, subsid int64, err error) {
	take := func(re *regexp.Regexp, what string) (int64, error) {
		m := re.FindStringSubmatch(page)
		if m == nil {
			return 0, fmt.Errorf("huya: %s not found in room page", what)
		}
		return strconv.ParseInt(m[1], 10, 64)
	}
	if ayyuid, err = take(reHuyaAyyuid, "ayyuid"); err != nil {
		return 0, 0, 0, err
	}
	if topsid, err = take(reHuyaTopsid, "TOPSID"); err != nil {
		return 0, 0, 0, err
	}
	if subsid, err = take(reHuyaSubsid, "SUBSID"); err != nil {
		return 0, 0, 0, err
	}
	return ayyuid, topsid, subsid, nil
}

// buildHuyaRegister builds the register packet: EWSCmd_RegisterReq with a
// WSUserInfo payload (lUid=ayyuid, lTid=TOPSID, lSid=SUBSID, anonymous), as
// the reference clients do.
func buildHuyaRegister(ayyuid, topsid, subsid int64) []byte {
	var userInfo tarsEncoder
	userInfo.writeInt64(0, ayyuid)
	userInfo.writeBool(1, true) // bAnonymous
	userInfo.writeString(2, "") // sGuid
	userInfo.writeString(3, "") // sToken
	userInfo.writeInt64(4, topsid)
	userInfo.writeInt64(5, subsid)
	userInfo.writeInt64(6, 0) // lGroupId
	userInfo.writeInt64(7, 0) // lGroupType

	var wscmd tarsEncoder
	wscmd.writeInt32(0, huyaCmdRegReq)
	wscmd.writeBytes(1, userInfo.buf)
	return wscmd.buf
}

func huyaHeartbeatLoop(ctx context.Context, ws *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := ws.Write(huyaHeartbeat); err != nil {
				ws.Close() // unblock the read loop so runOnce can reconnect
				return
			}
		}
	}
}

// decodeMsg decodes one websocket frame. Chat pushes are tars packets with
// EWSCmdS2C_MsgPushReq at tag 0; their payload nests iUri=1400 at tag 1 and
// the MessageNotice bytes at tag 2, where the sender struct sits at tag 0
// (nick at 2), the text at tag 3 and the bullet format (color) struct at
// tag 6. Everything else is skipped.
func (c *HuyaDanmakuClient) decodeMsg(data []byte) *DanmakuMessage {
	d := newTarsDecoder(data)
	if cmd, err := d.readInt(0); err != nil || cmd != huyaCmdPush {
		return nil
	}
	payload, err := d.readBytes(1)
	if err != nil {
		return nil
	}
	d = newTarsDecoder(payload)
	if msgType, err := d.readInt(1); err != nil || msgType != huyaMsgChat {
		return nil
	}
	body, err := d.readBytes(2)
	if err != nil {
		return nil
	}
	d = newTarsDecoder(body)

	nick := ""
	if user, err := d.readStruct(0); err == nil {
		_, _ = user.readInt(0) // uid
		_, _ = user.readInt(1) // imid
		if name, err := user.readString(2); err == nil {
			nick = name
		}
		_, _ = user.readInt(3) // gender
	}
	text, err := d.readString(3)
	if err != nil {
		return nil
	}
	color := 16777215
	if dm, err := d.readStruct(6); err == nil {
		if col, err := dm.readInt(0); err == nil && col > 0 {
			color = int(col)
		}
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return &DanmakuMessage{
		Nick:     nick,
		Text:     text,
		Color:    fmt.Sprintf("%06x", color),
		Position: 0,
		Time:     time.Now(),
	}
}

func (c *HuyaDanmakuClient) emit(msg *DanmakuMessage) {
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
