package app

import (
	"errors"
	"testing"
	"time"

	"revda/internal/engine/streamfinder"
	"revda/internal/player"
	"revda/internal/sites"

	"github.com/egoist/mygo/ui"
)

func TestStatusToastLifecycle(t *testing.T) {
	a := &App{}

	// A fresh toast shows and carries its message.
	a.SetStatus("hello", false)
	if got := a.statusMsg; got != "hello" {
		t.Fatalf("statusMsg = %q, want %q", got, "hello")
	}
	if a.statusUntil.IsZero() {
		t.Fatal("statusUntil is zero after SetStatus")
	}
	if !a.statusHideAt.IsZero() {
		t.Fatal("statusHideAt is set before the toast expires")
	}

	// DismissStatus hides it at once.
	a.DismissStatus()
	if a.statusMsg != "" {
		t.Fatalf("statusMsg = %q after DismissStatus, want empty", a.statusMsg)
	}
	if !a.statusUntil.IsZero() || !a.statusHideAt.IsZero() {
		t.Fatal("timers not cleared by DismissStatus")
	}
}

func TestStatusToastExpiry(t *testing.T) {
	a := &App{}

	a.SetStatus("expiring", false)
	a.mu.Lock()
	a.statusUntil = time.Now().Add(-time.Second)
	a.mu.Unlock()

	// Before the exit animation ends the toast still exists, so the exit
	// transition can play: statusMsg stays, the build flag moves to hideAt.
	showing, next := a.expireStatus(time.Now())
	if !showing {
		t.Fatal("toast stops showing during its exit animation")
	}
	if a.statusMsg != "expiring" {
		t.Fatalf("statusMsg = %q during exit, want it kept", a.statusMsg)
	}
	if a.statusHideAt.IsZero() {
		t.Fatal("statusHideAt not set once expired")
	}
	if !a.statusUntil.IsZero() {
		t.Fatal("statusUntil not cleared once expired")
	}
	if next.IsZero() {
		t.Fatal("no frame scheduled for the end of the exit animation")
	}

	// Once the exit animation has had its time, the toast is gone for good.
	a.mu.Lock()
	a.statusHideAt = time.Now().Add(-2 * statusExitDuration)
	a.mu.Unlock()
	showing, next = a.expireStatus(time.Now())
	if showing {
		t.Fatal("toast still showing after its exit animation ended")
	}
	if a.statusMsg != "" {
		t.Fatalf("statusMsg = %q after the exit, want empty", a.statusMsg)
	}
	if !a.statusHideAt.IsZero() || !next.IsZero() {
		t.Fatal("state not cleared after the exit")
	}
}

func TestStatusToastShowsWhileFresh(t *testing.T) {
	a := &App{}
	a.SetStatus("fresh", false)

	showing, next := a.expireStatus(time.Now())
	if !showing {
		t.Fatal("a fresh toast does not show")
	}
	if next.IsZero() {
		t.Fatal("no frame scheduled for a fresh toast's expiry")
	}
	if a.statusMsg != "fresh" {
		t.Fatalf("statusMsg = %q, want %q", a.statusMsg, "fresh")
	}
}

func TestStatusToastReplacesPrevious(t *testing.T) {
	a := &App{}

	a.SetStatus("first", true)
	a.mu.Lock()
	firstUntil := a.statusUntil
	a.mu.Unlock()

	time.Sleep(5 * time.Millisecond)
	a.SetStatus("second", false)

	if a.statusMsg != "second" {
		t.Fatalf("statusMsg = %q, want %q", a.statusMsg, "second")
	}
	if a.statusIsErr {
		t.Fatal("statusIsErr still true after a non-error status")
	}
	a.mu.RLock()
	replaced := a.statusUntil.After(firstUntil)
	a.mu.RUnlock()
	if !replaced {
		t.Fatal("statusUntil not pushed out by the newer status")
	}
}

func TestIsVideoCode(t *testing.T) {
	// Videos and bangumi have no live state, so playback skips the pre-check.
	for _, code := range []string{"BV1xx411c7mD", "av170001", "ep-123", "ss-456", "ytv-abc"} {
		if !isVideoCode(code) {
			t.Errorf("isVideoCode(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"bi-545342", "do-475252", "hu-660118", "yt-name", "tw-name"} {
		if isVideoCode(code) {
			t.Errorf("isVideoCode(%q) = true, want false", code)
		}
	}
}

func TestOfflineNoticeUsesCachedInfo(t *testing.T) {
	a := &App{roomInfos: map[string]*sites.RoomInfo{
		"do-475252": {RoomCode: "do-475252", IsLiving: false},
		"bi-545342": {RoomCode: "bi-545342", IsLiving: true},
	}}

	// A room the list already knows is offline needs no request.
	msg, offline := a.offlineNotice("do-475252")
	if !offline {
		t.Fatal("a known-offline room was not reported as offline")
	}
	if msg != "do-475252 未开播，主播当前没有直播" {
		t.Errorf("msg = %q", msg)
	}

	// A known-live room goes straight to playback.
	if _, offline := a.offlineNotice("bi-545342"); offline {
		t.Error("a live room was reported as offline")
	}
}

func TestPlayFailureMessageReadable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want string
	}{
		{
			name: "engine offline",
			err:  streamfinder.Offline("douyu returned no stream"),
			code: "do-475252",
			want: "do-475252 未开播，主播当前没有直播",
		},
		{
			name: "engine offline wrapped by resolve",
			err:  errors.New("resolve stream error: " + streamfinder.Offline("bilibili returned no live streams").Error()),
			code: "bi-545342",
			want: "bi-545342 未开播，主播当前没有直播",
		},
		{
			name: "plain engine error keeps the room name",
			err:  errors.New("douyu room475252 js encryption not found"),
			code: "do-475252",
			want: "播放 do-475252 失败：douyu room475252 js encryption not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := playFailureMessage(tt.err, tt.code); got != tt.want {
				t.Errorf("playFailureMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlayFailureMessageFromPlayError(t *testing.T) {
	// A PlayError carries its own sentence; the room code passed alongside is
	// only a fallback for errors that are not one.
	playErr := &player.PlayError{
		Kind:     player.ErrOffline,
		RoomCode: "hu-660118",
		Err:      streamfinder.Offline("huya streamer is not on"),
	}
	if got := playFailureMessage(playErr, "hu-660118"); got != "hu-660118 未开播，主播当前没有直播" {
		t.Errorf("message = %q", got)
	}
}

func TestPlatformIconMapping(t *testing.T) {
	tests := []struct {
		code string
		want bool // whether an icon is expected
	}{
		{"bi-545342", true},
		{"BV1xx411c7mD", true},
		{"av170001", true},
		{"do-475252", true},
		{"hu-660118", true},
		{"yt-somename", true},
		{"ytv-abc123", true},
		{"tw-someone", true},
		{"ep-123", true}, // bangumi lives on bilibili
		{"ss-456", true}, // so does this
		{"garbage", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := platformIcon(tt.code) != nil
			if got != tt.want {
				t.Errorf("platformIcon(%q) present = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestPlatformIconIsDistinctPerPlatform(t *testing.T) {
	// Each platform must get its own mark, not one shared icon.
	sources := map[string]*platformIconSource{
		"bilibili": platformIcon("bi-1"),
		"douyu":    platformIcon("do-1"),
		"huya":     platformIcon("hu-1"),
		"youtube":  platformIcon("yt-1"),
		"twitch":   platformIcon("tw-1"),
	}
	for name, src := range sources {
		if src == nil {
			t.Fatalf("%s has no icon", name)
		}
		if src.svg == nil && src.bitmap == nil {
			t.Errorf("%s icon has neither an SVG nor a bitmap", name)
		}
	}

	// No two platforms may share the same artwork.
	seen := map[any]string{}
	for name, src := range sources {
		var key any
		if src.svg != nil {
			key = src.svg
		} else {
			key = src.bitmap
		}
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s share the same icon", name, other)
		}
		seen[key] = name
	}
}

func TestPlatformIconUsesExpectedArtworkKind(t *testing.T) {
	// simple-icons ships single-color paths for these three; douyu and huya
	// only exist as the bitmaps their own sites serve.
	svgPlatforms := map[string]string{
		"bi-1":  "bilibili",
		"yt-1":  "youtube",
		"tw-1":  "twitch",
		"BV1xx": "bilibili",
		"ep-1":  "bilibili",
	}
	for code, name := range svgPlatforms {
		src := platformIcon(code)
		if src == nil || src.svg == nil {
			t.Errorf("%s (%s) should use an SVG", code, name)
		}
	}
	for code, name := range map[string]string{"do-1": "douyu", "hu-1": "huya"} {
		src := platformIcon(code)
		if src == nil || src.bitmap == nil {
			t.Errorf("%s (%s) should use a bitmap", code, name)
		}
	}
}

func TestPlatformIconsAreWellFormed(t *testing.T) {
	// Every SVG must parse and declare a square viewBox, or it renders at an
	// unexpected size and breaks the badge it sits in.
	svgs := map[string]*ui.SVG{
		"bilibili": iconBilibili,
		"youtube":  iconYoutube,
		"twitch":   iconTwitch,
	}
	for name, svg := range svgs {
		t.Run(name, func(t *testing.T) {
			w, h := svg.Size()
			if w <= 0 || h <= 0 {
				t.Fatalf("size = %vx%v", w, h)
			}
			if w != h {
				t.Errorf("size = %vx%v, want square so it scales evenly", w, h)
			}
		})
	}

	// The bitmaps must decode and be square too, for the same reason.
	bitmaps := map[string]*ui.Bitmap{
		"douyu": douyuBitmap,
		"huya":  huyaBitmap,
	}
	for name, bmp := range bitmaps {
		t.Run(name, func(t *testing.T) {
			if bmp == nil {
				t.Fatal("bitmap did not decode")
			}
			w, h := bmp.Size()
			if w <= 0 || h <= 0 {
				t.Fatalf("size = %vx%v", w, h)
			}
			if w != h {
				t.Errorf("size = %vx%v, want square so it scales evenly", w, h)
			}
		})
	}
}

func TestPlatformIconCoversEverySupportedPrefix(t *testing.T) {
	// A prefix the engine can play but the icon map does not know leaves a
	// card with no mark, which is the gap this change closes.
	for _, code := range []string{
		"bi-545342", "do-475252", "hu-660118",
		"yt-name", "ytv-abc123", "tw-name",
		"BV1xx411c7mD", "av170001", "ep-123", "ss-456",
	} {
		if platformIcon(code) == nil {
			t.Errorf("no icon for playable code %q", code)
		}
	}
}
