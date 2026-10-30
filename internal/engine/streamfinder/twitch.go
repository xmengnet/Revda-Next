package streamfinder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Twitch uses the same anonymous web GQL endpoint the Rust dmlive client uses;
// no OAuth token is required for playback access tokens so far (verified 2026-10).
const (
	twitchGQLAPI   = "https://gql.twitch.tv/gql"
	twitchClientID = "kimne78kx3ncx6brgo4mv6wki5h1ko"
	twitchUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

type TwitchFinder struct {
	client *http.Client
}

func NewTwitchFinder() *TwitchFinder {
	return &TwitchFinder{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *TwitchFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	u, err := url.Parse(roomURL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("invalid twitch channel url")
	}
	rid := strings.ToLower(parts[len(parts)-1])

	info, err := t.gql(ctx, fmt.Sprintf(`{ user(login: %q) { displayName stream { title } } }`, rid),
		nil)
	if err != nil {
		return nil, fmt.Errorf("twitch stream info: %w", err)
	}
	user, _ := info["user"].(map[string]any)
	if len(user) == 0 {
		return nil, ErrNotFound
	}
	owner, _ := user["displayName"].(string)
	if owner == "" {
		return nil, ErrNotFound
	}
	if user["stream"] == nil {
		return nil, Offline("twitch streamer is not live")
	}
	title := "没有直播标题"
	if st, ok := user["stream"].(map[string]any); ok {
		if s, _ := st["title"].(string); s != "" {
			title = s
		}
	}

	token, err := t.gql(ctx, fmt.Sprintf(
		`{ streamPlaybackAccessToken(channelName: %q, params: { platform: "web", playerBackend: "mediaplayer", playerType: "pulsar" }) { value signature } }`,
		rid), nil)
	if err != nil {
		return nil, fmt.Errorf("twitch playback token: %w", err)
	}
	pat, _ := token["streamPlaybackAccessToken"].(map[string]any)
	var sig, val string
	if pat != nil {
		sig, _ = pat["signature"].(string)
		val, _ = pat["value"].(string)
	}
	if sig == "" || val == "" {
		return nil, errors.New("twitch playback token empty")
	}

	q := url.Values{}
	q.Set("allow_source", "true")
	q.Set("fast_bread", "true")
	q.Set("sig", sig)
	q.Set("token", val)
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://usher.ttvnw.net/api/channel/hls/"+rid+".m3u8?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", twitchUA)
	req.Header.Set("Referer", "https://m.twitch.tv/")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twitch usher returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	variant, err := pickTwitchVariant(string(body))
	if err != nil {
		return nil, err
	}

	return &StreamInfo{
		Title: fmt.Sprintf("%s - %s", title, owner),
		URL:   variant,
		Headers: map[string]string{
			"User-Agent": twitchUA,
			"Referer":    "https://m.twitch.tv/",
		},
	}, nil
}

// gql posts a GraphQL query and returns the decoded "data" object.
func (t *TwitchFinder) gql(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", twitchGQLAPI, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", twitchClientID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", twitchUA)
	req.Header.Set("Referer", "https://m.twitch.tv/")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("twitch gql decode: %w", err)
	}
	if len(decoded.Errors) > 0 {
		return nil, fmt.Errorf("twitch gql error: %s", decoded.Errors[0].Message)
	}
	return decoded.Data, nil
}

// pickTwitchVariant returns the highest-bandwidth variant playlist URL from a
// twitch master m3u8.
func pickTwitchVariant(master string) (string, error) {
	reBW := regexp.MustCompile(`BANDWIDTH=(\d+)`)
	bestURL := ""
	bestBW := -1
	nextURI := false
	bw := 0
	for _, line := range strings.Split(master, "\n") {
		line = strings.TrimSpace(line)
		if nextURI {
			if line != "" && !strings.HasPrefix(line, "#") {
				if bw > bestBW {
					bestBW = bw
					bestURL = line
				}
				nextURI = false
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			if m := reBW.FindStringSubmatch(line); m != nil {
				bw, _ = strconv.Atoi(m[1])
				nextURI = true
			}
		}
	}
	if bestURL == "" {
		return "", errors.New("twitch master playlist has no variants")
	}
	return bestURL, nil
}
