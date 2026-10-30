package streamfinder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Youtube resolves live (and cipher-free VOD) streams. Two strategies:
//
//  1. Native: scrape ytInitialPlayerResponse from the webpage and use the
//     direct adaptiveFormats URLs. YouTube dropped dashManifestUrl (what the
//     Rust dmlive client still reads), and on bot-flagged egress IPs googlevideo
//     403s the WEB-client URLs, so every candidate URL is validated with a
//     ranged GET before use.
//  2. Fallback: shell out to yt-dlp when installed — it tracks YouTube's
//     moving bot-check target (visitor data, TV clients, PO tokens) so the app
//     does not have to. This is the path that actually works on datacenter/VPN
//     egress.
const (
	youtubeUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	youtubeReferer = "https://www.youtube.com/"
)

type YoutubeFinder struct {
	client *http.Client
}

func NewYoutubeFinder() *YoutubeFinder {
	return &YoutubeFinder{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

type ytFormat struct {
	Itag     int    `json:"itag"`
	MimeType string `json:"mimeType"`
	Bitrate  int    `json:"bitrate"`
	URL      string `json:"url"`
}

type ytPlayerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	VideoDetails struct {
		Title  string `json:"title"`
		Author string `json:"author"`
		IsLive bool   `json:"isLive"`
	} `json:"videoDetails"`
	StreamingData struct {
		AdaptiveFormats []ytFormat `json:"adaptiveFormats"`
	} `json:"streamingData"`
}

// Resolve accepts a channel-live URL (https://www.youtube.com/@handle/live)
// or a video URL (https://www.youtube.com/watch?v=...). Channel URLs require
// the streamer to be live.
func (y *YoutubeFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	info, nativeErr := y.resolveNative(ctx, roomURL)
	if nativeErr == nil {
		return info, nil
	}
	if errors.Is(nativeErr, ErrOffline) {
		return nil, nativeErr // no point asking yt-dlp about an offline channel
	}

	fallbackInfo, ytdlpErr := y.resolveYtDlp(ctx, roomURL)
	if ytdlpErr != nil {
		return nil, fmt.Errorf("%v; yt-dlp fallback also failed: %w", nativeErr, ytdlpErr)
	}
	return fallbackInfo, nil
}

func (y *YoutubeFinder) resolveNative(ctx context.Context, roomURL string) (*StreamInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", roomURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", youtubeUA)
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("Referer", youtubeReferer)

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	raw, ok := extractJSONObject(string(body), regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*`))
	if !ok {
		return nil, errors.New("youtube: ytInitialPlayerResponse not found in page")
	}
	var pr ytPlayerResponse
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		return nil, fmt.Errorf("youtube: parse player response: %w", err)
	}

	if pr.PlayabilityStatus.Status != "OK" && pr.PlayabilityStatus.Status != "" {
		reason := pr.PlayabilityStatus.Reason
		if reason == "" {
			reason = pr.PlayabilityStatus.Status
		}
		return nil, fmt.Errorf("youtube: %s", reason)
	}

	// Channel pages fall back to a trailer VOD when offline; never mistake
	// that for the live stream.
	if strings.Contains(roomURL, "/live") && !pr.VideoDetails.IsLive {
		return nil, Offline("youtube channel is not live")
	}

	video := pickYTFormat(pr.StreamingData.AdaptiveFormats, "video/")
	audio := pickYTFormat(pr.StreamingData.AdaptiveFormats, "audio/")
	if video.URL == "" || audio.URL == "" {
		return nil, errors.New("youtube: no direct-url formats (signature-ciphered or bot check)")
	}

	// WEB-client googlevideo URLs 403 on bot-flagged egress; catch it here
	// instead of letting mpv fail silently later.
	if !validateGoogleVideoURL(ctx, y.client, video.URL) || !validateGoogleVideoURL(ctx, y.client, audio.URL) {
		return nil, errors.New("youtube: googlevideo rejected the direct URLs (403)")
	}

	title := pr.VideoDetails.Title
	if title == "" {
		title = "没有直播标题"
	}
	return &StreamInfo{
		Title:    fmt.Sprintf("%s - %s", title, pr.VideoDetails.Author),
		URL:      video.URL,
		AudioURL: audio.URL,
		Headers: map[string]string{
			"User-Agent": youtubeUA,
			"Referer":    youtubeReferer,
		},
	}, nil
}

func validateGoogleVideoURL(ctx context.Context, client *http.Client, u string) bool {
	vc := *client
	vc.Timeout = 8 * time.Second
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-1023")
	req.Header.Set("User-Agent", youtubeUA)
	resp, err := vc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent
}

type ytDlpFormat struct {
	URL    string  `json:"url"`
	VCodec string  `json:"vcodec"`
	ACodec string  `json:"acodec"`
	TBR    float64 `json:"tbr"`
	ABR    float64 `json:"abr"`
	Height int     `json:"height"`
}

type ytDlpDump struct {
	Title    string        `json:"title"`
	Uploader string        `json:"uploader"`
	IsLive   bool          `json:"is_live"`
	Formats  []ytDlpFormat `json:"formats"`
}

// resolveYtDlp shells out to yt-dlp -J, the community-maintained answer to
// YouTube's bot checks.
func (y *YoutubeFinder) resolveYtDlp(ctx context.Context, roomURL string) (*StreamInfo, error) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		return nil, errors.New("yt-dlp not installed (needed when YouTube blocks direct access)")
	}
	cmd := exec.CommandContext(ctx, bin, "-J", "--no-warnings", roomURL)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp: %w", err)
	}
	var dump ytDlpDump
	if err := json.Unmarshal(out, &dump); err != nil {
		return nil, fmt.Errorf("yt-dlp json: %w", err)
	}

	// Prefer a combined A/V stream (single URL, no EDL needed).
	var bestCombined, bestVideo, bestAudio ytDlpFormat
	for _, it := range dump.Formats {
		if it.URL == "" {
			continue
		}
		hasV := it.VCodec != "" && it.VCodec != "none"
		hasA := it.ACodec != "" && it.ACodec != "none"
		if hasV && hasA && it.TBR > bestCombined.TBR {
			bestCombined = it
		}
		if hasV && !hasA && it.TBR > bestVideo.TBR {
			bestVideo = it
		}
		if hasA && !hasV && it.ABR+it.TBR > bestAudio.ABR+bestAudio.TBR {
			bestAudio = it
		}
	}

	ret := &StreamInfo{Headers: map[string]string{
		"User-Agent": youtubeUA,
		"Referer":    youtubeReferer,
	}}
	switch {
	case bestCombined.URL != "":
		ret.URL = bestCombined.URL
	case bestVideo.URL != "" && bestAudio.URL != "":
		ret.URL, ret.AudioURL = bestVideo.URL, bestAudio.URL
	case bestVideo.URL != "":
		ret.URL = bestVideo.URL
	default:
		if dump.IsLive {
			return nil, errors.New("yt-dlp returned no downloadable formats for the live stream")
		}
		return nil, errors.New("yt-dlp returned no usable formats")
	}
	ret.Title = fmt.Sprintf("%s - %s", dump.Title, dump.Uploader)
	return ret, nil
}

// pickYTFormat picks the highest-bitrate format with the wanted mime prefix,
// preferring mp4 containers for player compatibility. Returns the zero format
// (URL == "") when nothing is usable.
func pickYTFormat(formats []ytFormat, mimePrefix string) ytFormat {
	bestMP4, bestAny := ytFormat{}, ytFormat{}
	for _, f := range formats {
		if f.URL == "" {
			continue // signatureCipher variants need JS deciphering
		}
		mime := strings.ToLower(f.MimeType)
		if !strings.HasPrefix(mime, mimePrefix) {
			continue
		}
		if bestAny.URL == "" || f.Bitrate > bestAny.Bitrate {
			bestAny = f
		}
		if strings.Contains(mime, "mp4") && (bestMP4.URL == "" || f.Bitrate > bestMP4.Bitrate) {
			bestMP4 = f
		}
	}
	if bestMP4.URL != "" {
		return bestMP4
	}
	return bestAny
}

// extractJSONObject finds `marker { ... }` in an HTML page and returns the
// balanced JSON object (regex alone cannot capture it reliably).
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
