package danmaku

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const twitchWSURL = "wss://irc-ws.chat.twitch.tv"

var (
	reTwitchNick  = regexp.MustCompile(`display-name=([^;]+);`)
	reTwitchText  = regexp.MustCompile(`PRIVMSG [^:]+:(.+)`)
	reTwitchColor = regexp.MustCompile(`color=#([a-zA-Z0-9]{6});`)
)

type TwitchDanmakuClient struct {
	roomID string

	messages chan *DanmakuMessage
}

func NewTwitchDanmakuClient(roomID string) *TwitchDanmakuClient {
	return &TwitchDanmakuClient{
		roomID:   roomID,
		messages: make(chan *DanmakuMessage, 200),
	}
}

func (c *TwitchDanmakuClient) Messages() <-chan *DanmakuMessage {
	return c.messages
}

func (c *TwitchDanmakuClient) Start(ctx context.Context) error {
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

func (c *TwitchDanmakuClient) runOnce(ctx context.Context) error {
	cfg, err := websocket.NewConfig(twitchWSURL, "https://www.twitch.tv")
	if err != nil {
		return err
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return err
	}
	defer ws.Close()
	ws.PayloadType = websocket.TextFrame

	// Anonymous login (justinfan accounts can read chat but not send).
	nick := fmt.Sprintf("justinfan%d", 10000+rand.Int63n(80000))
	for _, reg := range []string{
		"CAP REQ :twitch.tv/tags twitch.tv/commands twitch.tv/membership",
		"PASS SCHMOOPIIE",
		"NICK " + nick,
		fmt.Sprintf("USER %s 8 * :%s", nick, nick),
		"JOIN #" + strings.ToLower(c.roomID),
	} {
		if _, err := ws.Write([]byte(reg)); err != nil {
			return err
		}
	}

	// The read has no deadline (quiet rooms send nothing); ctx cancellation
	// and heartbeat write failures close the conn to unblock it.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		<-hbCtx.Done()
		ws.Close()
	}()
	go twitchHeartbeatLoop(hbCtx, ws)

	buf := make([]byte, 1<<16)
	for {
		n, err := ws.Read(buf) // one websocket message per read
		if err != nil {
			return err
		}
		// One frame can batch several CRLF-separated IRC lines.
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "PING") {
				// The server pings us every few minutes and drops the
				// connection unless we answer.
				ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if _, err := ws.Write([]byte("PONG :tmi.twitch.tv")); err != nil {
					return err
				}
				continue
			}
			if msg := decodeTwitchLine(line); msg != nil {
				c.emit(msg)
			}
		}
	}
}

func twitchHeartbeatLoop(ctx context.Context, ws *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := ws.Write([]byte("PING")); err != nil {
				ws.Close() // unblock the read loop so runOnce can reconnect
				return
			}
		}
	}
}

// decodeTwitchLine parses one IRC line. Chat arrives as PRIVMSG with IRCv3
// tags; everything else (JOIN, USERNOTICE, our own PONG…) has no display-name
// tag and is skipped.
func decodeTwitchLine(line string) *DanmakuMessage {
	nick := reTwitchNick.FindStringSubmatch(line)
	if nick == nil {
		return nil
	}
	text := reTwitchText.FindStringSubmatch(line)
	if text == nil {
		return nil
	}
	color := "ffffff"
	if m := reTwitchColor.FindStringSubmatch(line); m != nil {
		color = m[1]
	}
	msgText := strings.TrimSpace(text[1])
	if msgText == "" {
		return nil
	}
	return &DanmakuMessage{
		Nick:     nick[1],
		Text:     msgText,
		Color:    color,
		Position: 0,
		Time:     time.Now(),
	}
}

func (c *TwitchDanmakuClient) emit(msg *DanmakuMessage) {
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
