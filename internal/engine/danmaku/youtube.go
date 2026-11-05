package danmaku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// YouTube live chat via the inner-tube live_chat API, ported from Rust
// dmlive's danmaku/youtube.rs: a self-built protobuf continuation requests
// the next chat page every 2s.
const ytbKeyB64 = "eW91dHViZWkvdjEvbGl2ZV9jaGF0L2dldF9saXZlX2NoYXQ/a2V5PUFJemFTeUFPX0ZKMlNscVU4UTRTVEVITEdDaWx3X1k5XzExcWNXOA=="

type YoutubeDanmakuClient struct {
	roomURL string

	messages chan *DanmakuMessage
}

func NewYoutubeDanmakuClient(roomURL string) *YoutubeDanmakuClient {
	return &YoutubeDanmakuClient{
		roomURL:  roomURL,
		messages: make(chan *DanmakuMessage, 200),
	}
}

func (c *YoutubeDanmakuClient) Messages() <-chan *DanmakuMessage {
	return c.messages
}

func (c *YoutubeDanmakuClient) Start(ctx context.Context) error {
	backoff := 3 * time.Second
	for {
		if err := c.runOnce(ctx); err != nil && ctx.Err() == nil {
			// keep the error for the next loop's backoff decision
			_ = err
		}
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

func (c *YoutubeDanmakuClient) emit(nick, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	select {
	case c.messages <- &DanmakuMessage{Nick: nick, Text: strings.TrimSpace(text), Color: "ffffff", Time: time.Now()}:
	default:
	}
}

// --- minimal protobuf wire encoding (utils::vn/tp/rs/nm in Rust dmlive) ---

func pbVarint(v uint64) []byte {
	var buf []byte
	for v>>7 != 0 {
		buf = append(buf, byte(v&0xFF|0x80))
		v >>= 7
	}
	return append(buf, byte(v))
}

func pbTag(field, wire int) []byte {
	return pbVarint(uint64(field<<3 | wire))
}

func pbBytes(field int, b []byte) []byte {
	out := pbTag(field, 2)
	out = append(out, pbVarint(uint64(len(b)))...)
	return append(out, b...)
}

func pbUint(field int, v uint64) []byte {
	return append(pbTag(field, 0), pbVarint(v)...)
}

// buildContinuation assembles the get_live_chat continuation parameter.
func buildContinuation(vid, cid string) string {
	ts := uint64(time.Now().UnixNano() / 1000) // µs
	chatType := uint64(1)

	s1_3 := pbBytes(1, []byte(vid))
	s1_5 := append(pbBytes(1, []byte(cid)), pbBytes(2, []byte(vid))...)
	s1 := append(pbBytes(3, s1_3), pbBytes(5, s1_5)...)
	s3 := pbBytes(48687757, pbBytes(1, []byte(vid)))
	header := append(append(pbBytes(1, s1), pbBytes(3, s3)...), pbUint(4, 1)...)
	header = pbBytes(3, []byte(base64.StdEncoding.EncodeToString(header)))

	var body []byte
	body = append(body, pbUint(1, 0)...)    // b1
	body = append(body, pbUint(2, 0)...)    // b2
	body = append(body, pbUint(3, 0)...)    // b3
	body = append(body, pbUint(4, 0)...)    // b4
	body = append(body, pbBytes(7, nil)...) // b7
	body = append(body, pbUint(8, 0)...)    // b8
	body = append(body, pbBytes(9, nil)...) // b9
	body = append(body, pbUint(10, ts)...)  // timestamp2
	body = append(body, pbUint(11, 3)...)   // b11
	body = append(body, pbUint(15, 0)...)   // b15
	body = pbBytes(9, body)

	entity := append([]byte{}, header...)
	entity = append(entity, pbUint(5, ts)...) // timestamp1
	entity = append(entity, pbUint(6, 0)...)  // s6
	entity = append(entity, pbUint(7, 0)...)  // s7
	entity = append(entity, pbUint(8, 1)...)  // s8
	entity = append(entity, body...)
	entity = append(entity, pbUint(10, ts)...) // timestamp3
	entity = append(entity, pbUint(11, ts)...) // timestamp4
	entity = append(entity, pbUint(13, chatType)...)
	entity = append(entity, pbBytes(16, pbUint(1, chatType))...)
	entity = append(entity, pbUint(17, 0)...)             // s17
	entity = append(entity, pbBytes(19, pbUint(1, 0))...) // str19
	entity = append(entity, pbUint(20, ts)...)            // timestamp5

	continuation := pbBytes(119693434, entity)
	return url.QueryEscape(base64.URLEncoding.EncodeToString(continuation))
}

func (c *YoutubeDanmakuClient) getRoomInfo(ctx context.Context, client *http.Client) (vid, cid string, err error) {
	roomURL := c.roomURL
	if strings.Contains(roomURL, "youtube.com/@") {
		// The URL is either .../@handle or .../@handle/live; take the @segment
		// rather than the last path element.
		if u, perr := url.Parse(roomURL); perr == nil {
			handle := ""
			for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
				if strings.HasPrefix(seg, "@") {
					handle = strings.TrimPrefix(seg, "@")
					break
				}
			}
			if handle != "" {
				roomURL = "https://www.youtube.com/@" + handle + "/live"
			}
		}
	} else if u, perr := url.Parse(roomURL); perr == nil {
		if v := u.Query().Get("v"); v != "" {
			roomURL = "https://www.youtube.com/watch?v=" + v
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", roomURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("Referer", "https://www.youtube.com/")
	req.Header.Set("User-Agent", ytUA)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 32*1024)
	for len(buf) < 8<<20 {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}

	raw, ok := extractJSONObject(string(buf), ytInitialPlayerResponseRe)
	if !ok {
		return "", "", fmt.Errorf("youtube chat: player response not found")
	}
	var pr struct {
		VideoDetails struct {
			VideoId   string `json:"videoId"`
			ChannelId string `json:"channelId"`
		} `json:"videoDetails"`
	}
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		return "", "", err
	}
	if pr.VideoDetails.VideoId == "" || pr.VideoDetails.ChannelId == "" {
		return "", "", fmt.Errorf("youtube chat: video/channel id missing")
	}
	return pr.VideoDetails.VideoId, pr.VideoDetails.ChannelId, nil
}

var ytInitialPlayerResponseRe = regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*`)

// extractJSONObject returns the balanced JSON object that follows marker. The
// page embeds it in an inline <script> without a stable terminator, so brace
// counting (string- and escape-aware) beats a regex here.
func extractJSONObject(s string, marker *regexp.Regexp) (string, bool) {
	loc := marker.FindStringIndex(s)
	if loc == nil {
		return "", false
	}
	start := strings.IndexByte(s[loc[1]:], '{')
	if start < 0 {
		return "", false
	}
	start += loc[1]

	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

var ytUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:146.0) Gecko/20100101 Firefox/146.0"

func (c *YoutubeDanmakuClient) runOnce(ctx context.Context) error {
	key, err := base64.StdEncoding.DecodeString(ytbKeyB64)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	vid, cid, err := c.getRoomInfo(ctx, client)
	if err != nil {
		return err
	}

	ctn := buildContinuation(vid, cid)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if strings.TrimSpace(ctn) == "" {
			ctn = buildContinuation(vid, cid)
		}
		dms, next, err := c.getSingleChat(ctx, client, string(key), ctn)
		if err != nil {
			ctn = "" // regenerate next round, like Rust
			continue
		}
		ctn = next
		if len(dms) == 0 {
			continue
		}
		itvl := 2000 / len(dms)
		for _, m := range dms {
			c.emit(m.Nick, m.Text)
			switch {
			case itvl < 50:
			case itvl > 500:
				time.Sleep(500 * time.Millisecond)
			default:
				time.Sleep(time.Duration(itvl) * time.Millisecond)
			}
		}
	}
}

func (c *YoutubeDanmakuClient) getSingleChat(ctx context.Context, client *http.Client, key, ctn string) (msgs []*DanmakuMessage, next string, err error) {
	body := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"visitorData":   "",
				"userAgent":     ytUA,
				"clientName":    "WEB",
				"clientVersion": fmt.Sprintf("2.%s.01.00", time.Now().AddDate(0, 0, -2).UTC().Format("20060102")),
			},
		},
		"continuation": ctn,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://www.youtube.com/"+key, strings.NewReader(string(raw)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var out struct {
		ContinuationContents struct {
			LiveChatContinuation struct {
				Continuations []map[string]json.RawMessage `json:"continuations"`
				Actions       []json.RawMessage            `json:"actions"`
			} `json:"liveChatContinuation"`
		} `json:"continuationContents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", err
	}
	if len(out.ContinuationContents.LiveChatContinuation.Continuations) == 0 {
		return nil, "", fmt.Errorf("youtube chat: no continuations")
	}
	findToken := func(m map[string]json.RawMessage) (string, bool) {
		for _, wrapper := range []string{"invalidationContinuationData", "timedContinuationData", "reloadContinuationData", "liveChatReplayContinuationData"} {
			if v, ok := m[wrapper]; ok {
				var cd struct {
					Continuation string `json:"continuation"`
				}
				if json.Unmarshal(v, &cd) == nil && cd.Continuation != "" {
					return cd.Continuation, true
				}
			}
		}
		return "", false
	}
	next, ok := findToken(out.ContinuationContents.LiveChatContinuation.Continuations[0])
	if !ok {
		return nil, "", fmt.Errorf("youtube chat: continuation token missing")
	}

	for _, action := range out.ContinuationContents.LiveChatContinuation.Actions {
		var a struct {
			AddChatItemAction struct {
				Item struct {
					Renderer struct {
						AuthorName struct {
							SimpleText string `json:"simpleText"`
						} `json:"authorName"`
						Message struct {
							Runs []json.RawMessage `json:"runs"`
						} `json:"message"`
					} `json:"liveChatTextMessageRenderer"`
				} `json:"item"`
			} `json:"addChatItemAction"`
		}
		if json.Unmarshal(action, &a) != nil {
			continue
		}
		r := a.AddChatItemAction.Item.Renderer
		if r.AuthorName.SimpleText == "" {
			continue
		}
		var sb strings.Builder
		for _, run := range r.Message.Runs {
			var one struct {
				Text  string `json:"text"`
				Emoji struct {
					Shortcuts []string `json:"shortcuts"`
				} `json:"emoji"`
			}
			if json.Unmarshal(run, &one) != nil {
				continue
			}
			if one.Text != "" {
				sb.WriteString(one.Text)
			} else if len(one.Emoji.Shortcuts) > 0 {
				sb.WriteString(one.Emoji.Shortcuts[0])
			}
		}
		msgs = append(msgs, &DanmakuMessage{
			Nick:  r.AuthorName.SimpleText,
			Text:  strings.TrimSpace(sb.String()),
			Color: "ffffff",
			Time:  time.Now(),
		})
	}
	return msgs, next, nil
}
