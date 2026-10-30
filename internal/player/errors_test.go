package player

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"revda/internal/engine/streamfinder"
)

func TestClassifySentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"offline sentinel", streamfinder.ErrOffline, ErrOffline},
		{"offline wrapped", streamfinder.Offline("bilibili returned no live streams"), ErrOffline},
		{"offline through resolve wrap", fmt.Errorf("resolve stream error: %w", streamfinder.Offline("twitch streamer is not live")), ErrOffline},
		{"not found sentinel", streamfinder.ErrNotFound, ErrNotFound},
		{"geo blocked sentinel", streamfinder.ErrGeoBlocked, ErrGeoBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.err); got != tt.want {
				t.Errorf("classify(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyNetworkAndTextErrors(t *testing.T) {
	if got := classify(&net.DNSError{Err: "no such host", IsNotFound: true}); got != ErrNetwork {
		t.Errorf("dns error classified as %v, want network", got)
	}
	if got := classify(errors.New("context deadline exceeded")); got != ErrNetwork {
		t.Errorf("timeout classified as %v, want network", got)
	}
	// A technical failure with no offline wording must not be reported as the
	// streamer being offline.
	if got := classify(errors.New("douyu room475252 js encryption not found")); got == ErrOffline {
		t.Error("a JS-encryption failure was classified as offline")
	}
}

func TestPlayErrorMessageNamesRoom(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want string
	}{
		{
			name: "offline",
			err:  &PlayError{Kind: ErrOffline, RoomCode: "do-475252", Err: streamfinder.Offline("douyu returned no stream")},
			code: "do-475252",
			want: "do-475252 未开播，主播当前没有直播",
		},
		{
			name: "not found",
			err:  &PlayError{Kind: ErrNotFound, RoomCode: "bi-99999999", Err: streamfinder.ErrNotFound},
			code: "bi-99999999",
			want: "bi-99999999 不存在或已失效，请检查直播间代码是否正确",
		},
		{
			name: "network",
			err:  &PlayError{Kind: ErrNetwork, RoomCode: "hu-660118", Err: errors.New("dial tcp: connection refused")},
			code: "hu-660118",
			want: "连接 hu-660118 失败，请检查网络后重试",
		},
		{
			name: "mpv missing",
			err:  &PlayError{Kind: ErrPlayerMissing, RoomCode: "bi-545342", Err: errors.New("exec: mpv: executable file not found")},
			code: "bi-545342",
			want: "未找到播放器 mpv，请先安装后再试",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.(*PlayError).Message(); got != tt.want {
				t.Errorf("Message() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrapPlayErrorKeepsExistingPlayError(t *testing.T) {
	first := &PlayError{Kind: ErrOffline, RoomCode: "do-1", Err: streamfinder.Offline("x")}
	wrapped := fmt.Errorf("resolve stream error: %w", first)
	again := wrapPlayError(wrapped, "斗鱼", "do-1")

	var playErr *PlayError
	if !errors.As(again, &playErr) {
		t.Fatal("wrapPlayError dropped the PlayError")
	}
	if playErr.Kind != ErrOffline {
		t.Errorf("kind = %v, want offline", playErr.Kind)
	}
	if playErr.Message() != "do-1 未开播，主播当前没有直播" {
		t.Errorf("message = %q", playErr.Message())
	}
}

func TestWrapPlayErrorNil(t *testing.T) {
	if err := wrapPlayError(nil, "B站", "bi-1"); err != nil {
		t.Fatalf("wrapPlayError(nil) = %v, want nil", err)
	}
}

func TestPlatformOf(t *testing.T) {
	tests := map[string]string{
		"bi-545342":    "B站",
		"BV1xx411c7mD": "B站",
		"av170001":     "B站",
		"do-475252":    "斗鱼",
		"hu-660118":    "虎牙",
		"yt-somename":  "YouTube",
		"ytv-abc123":   "YouTube",
		"tw-someone":   "Twitch",
		"ep-123":       "",
	}

	for code, want := range tests {
		if got := platformOf(code); got != want {
			t.Errorf("platformOf(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestOfflineErrorIsAndUnwraps(t *testing.T) {
	err := streamfinder.Offline("huya streamer is not on")
	if !errors.Is(err, streamfinder.ErrOffline) {
		t.Error("errors.Is failed to match the offline sentinel")
	}
	if err.Error() == "" {
		t.Error("OfflineError has no message")
	}
}
