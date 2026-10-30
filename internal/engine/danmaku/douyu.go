package danmaku

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const (
	douyuWSURL   = "wss://danmuproxy.douyu.com:8505"
	douyuReferer = "https://www.douyu.com"
)

var douyuColors = map[string]string{
	"1": "ff0000",
	"2": "1e87f0",
	"3": "7ac84b",
	"4": "ff7f00",
	"5": "9b39f4",
	"6": "ff69b4",
}

type DouyuDanmakuClient struct {
	roomID string

	messages chan *DanmakuMessage
}

func NewDouyuDanmakuClient(roomID string) *DouyuDanmakuClient {
	return &DouyuDanmakuClient{
		roomID:   roomID,
		messages: make(chan *DanmakuMessage, 200),
	}
}

func (c *DouyuDanmakuClient) Messages() <-chan *DanmakuMessage {
	return c.messages
}

func (c *DouyuDanmakuClient) Start(ctx context.Context) error {
	backoff := 3 * time.Second
	for {
		c.runOnce(ctx)
		// Douyu filters TLS fingerprints dynamically: the same client may be
		// rejected a few times in a row before being let through.
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

func (c *DouyuDanmakuClient) runOnce(ctx context.Context) error {
	cfg, err := websocket.NewConfig(douyuWSURL, douyuReferer)
	if err != nil {
		return err
	}
	cfg.Header.Set("User-Agent", userAgent)
	// Go's default TLS 1.3 ClientHello gets fingerprint-blocked by danmuproxy;
	// TLS 1.2 with a conventional cipher list gets through.
	cfg.TlsConfig = &tls.Config{
		ServerName: "danmuproxy.douyu.com",
		MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		},
	}

	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return err
	}
	defer ws.Close()
	ws.PayloadType = websocket.BinaryFrame

	// loginreq + joingroup
	if _, err := ws.Write(c.encodePacket(fmt.Sprintf("type@=loginreq/roomid@=%s/", c.roomID))); err != nil {
		return err
	}
	if _, err := ws.Write(c.encodePacket(fmt.Sprintf("type@=joingroup/rid@=%s/gid@=1/", c.roomID))); err != nil {
		return err
	}

	// heartbeat every 20s keeps the session alive; it stops when the read loop
	// ends, so a dead connection no longer leaks the ticker.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		hb := c.encodePacket("type@=mrl/")
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if _, err := ws.Write(hb); err != nil {
					return
				}
			}
		}
	}()

	buf := make([]byte, 65536)
	var streamBuf []byte
	for {
		if ctx.Err() != nil {
			return nil
		}
		ws.SetReadDeadline(time.Now().Add(70 * time.Second))
		n, err := ws.Read(buf)
		if err != nil {
			return err
		}
		streamBuf = append(streamBuf, buf[:n]...)

		for len(streamBuf) >= 12 {
			msgLen := int(binary.LittleEndian.Uint32(streamBuf[0:4]))
			packetTotal := msgLen + 4
			if msgLen < 12 || len(streamBuf) < packetTotal {
				break
			}
			payload := streamBuf[12 : packetTotal-1] // strip trailing '\0'
			c.parsePayload(string(payload))
			streamBuf = streamBuf[packetTotal:]
		}
		if len(streamBuf) > 1<<20 { // server sent garbage framing, resync
			streamBuf = nil
		}
	}
}

func (c *DouyuDanmakuClient) encodePacket(payload string) []byte {
	length := uint32(len(payload) + 9)
	buf := make([]byte, length+4)
	binary.LittleEndian.PutUint32(buf[0:4], length)
	binary.LittleEndian.PutUint32(buf[4:8], length)
	buf[8] = 0xb1
	buf[9] = 0x02
	buf[10] = 0x00
	buf[11] = 0x00
	copy(buf[12:], payload)
	buf[len(buf)-1] = 0x00
	return buf
}

func (c *DouyuDanmakuClient) parsePayload(raw string) {
	// Douyu STT format: key@=value/key@=value/
	fields := strings.Split(raw, "/")
	dict := make(map[string]string, len(fields))
	for _, f := range fields {
		if k, v, ok := strings.Cut(f, "@="); ok {
			v = strings.ReplaceAll(v, "@A", "@")
			v = strings.ReplaceAll(v, "@S", "/")
			dict[k] = v
		}
	}

	if dict["type"] == "chatmsg" {
		text := dict["txt"]
		nick := dict["nn"]
		col := dict["col"]
		colorHex := douyuColors[col]
		if colorHex == "" {
			colorHex = "ffffff"
		}
		c.emit(nick, text, colorHex, 0)
	}
}

func (c *DouyuDanmakuClient) emit(nick, text, color string, position int) {
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
