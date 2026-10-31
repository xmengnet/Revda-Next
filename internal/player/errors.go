package player

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"revda/internal/engine/streamfinder"
)

// PlayError is why a playback attempt failed, in terms a user can act on.
// The engine's own errors are technical — "no live streams found in bilibili
// response", "douyu stream not found" — and mean the same thing across every
// platform: the streamer is not live. Kind carries that meaning so the UI can
// show one clear sentence instead of a stack of per-platform wording.
type PlayError struct {
	// Kind is the category of failure.
	Kind ErrorKind
	// Platform is the site the attempt was for, as it shows in the room list.
	Platform string
	// RoomCode is the room that failed, for the message to name.
	RoomCode string
	// Err is the engine's error, kept for logs.
	Err error
}

type ErrorKind int

const (
	// ErrOffline is the room not being live right now.
	ErrOffline ErrorKind = iota
	// ErrNotFound is the room code pointing at nothing.
	ErrNotFound
	// ErrNetwork is a connection or timeout failure.
	ErrNetwork
	// ErrGeoBlocked is the stream being unavailable in this region.
	ErrGeoBlocked
	// ErrPlayerMissing is mpv not being installed.
	ErrPlayerMissing
	// ErrUnsupported is the Go engine not handling this input.
	ErrUnsupported
	// ErrUnknown is anything else.
	ErrUnknown
)

func (e *PlayError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Kind.String()
}

func (e *PlayError) Unwrap() error { return e.Err }

// Message is what the user reads: one sentence naming the room and the cause,
// without engine internals.
func (e *PlayError) Message() string {
	room := e.RoomCode
	if room == "" {
		room = "该房间"
	}
	switch e.Kind {
	case ErrOffline:
		return fmt.Sprintf("%s 未开播，主播当前没有直播", room)
	case ErrNotFound:
		return fmt.Sprintf("%s 不存在或已失效，请检查直播间代码是否正确", room)
	case ErrNetwork:
		return fmt.Sprintf("连接 %s 失败，请检查网络后重试", room)
	case ErrGeoBlocked:
		return fmt.Sprintf("%s 的直播在当前地区不可播放", room)
	case ErrPlayerMissing:
		return "未找到播放器 mpv，请先安装后再试"
	case ErrUnsupported:
		return fmt.Sprintf("暂不支持播放 %s，可尝试右键使用其他引擎", room)
	default:
		return fmt.Sprintf("播放 %s 失败：%v", room, e.Err)
	}
}

func (k ErrorKind) String() string {
	switch k {
	case ErrOffline:
		return "offline"
	case ErrNotFound:
		return "not found"
	case ErrNetwork:
		return "network"
	case ErrGeoBlocked:
		return "geo blocked"
	case ErrPlayerMissing:
		return "player missing"
	case ErrUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// ClassifyError maps an engine error onto a Kind. Sentinel errors from the
// finders are authoritative; the wording match below is a fallback for the
// errors that still come through as plain text.
func ClassifyError(err error) ErrorKind {
	return classify(err)
}

// classify maps an engine error onto a Kind. Sentinel errors from the finders
// are authoritative; the wording match below is a fallback for the errors that
// still come through as plain text.
func classify(err error) ErrorKind {
	if err == nil {
		return ErrUnknown
	}

	switch {
	case errors.Is(err, streamfinder.ErrOffline):
		return ErrOffline
	case errors.Is(err, streamfinder.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, streamfinder.ErrGeoBlocked):
		return ErrGeoBlocked
	}

	// Transport failures come before any wording match: a DNS error reads
	// "no such host" and must not be taken for a missing room.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ErrNetwork
	}

	msg := strings.ToLower(err.Error())

	// A technical lookup that came up empty is a failure of the engine, not a
	// missing room: "js encryption not found" must not read as the room being
	// gone. These keep the raw error, which names what was missing.
	if containsAny(msg, "js encryption not found", "player config not found",
		"encryption not found", "playback token empty", "master playlist",
		"no direct-url formats", "ytinitialplayerresponse", "signature-ciphered",
		"no downloadable formats", "no usable formats") {
		return ErrUnknown
	}

	switch {
	case containsAny(msg, "offline", "not live", "no live", "未开播", "not found (streamer", "stream not found", "videoLoop", "show_status", "is_on", "live_status"):
		return ErrOffline
	case containsAny(msg, "not found", "不存在", "no such", "invalid room", "channel not found", "unknown room"):
		return ErrNotFound
	case containsAny(msg, "geo", "region", "地区", "blocked in", "not available in your"):
		return ErrGeoBlocked
	case containsAny(msg, "mpv", "executable file not found"):
		return ErrPlayerMissing
	case containsAny(msg, "timeout", "deadline exceeded", "connection refused", "connection reset",
		"no route to host", "dial tcp", "eof", "tls", "certificate"):
		return ErrNetwork
	}
	return ErrUnknown
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// wrapPlayError attaches the room and platform to err, classifying it. It
// returns nil for a nil error so callers can pass a result straight through.
func wrapPlayError(err error, platform, roomCode string) error {
	if err == nil {
		return nil
	}
	var playErr *PlayError
	if errors.As(err, &playErr) {
		return err
	}
	return &PlayError{Kind: classify(err), Platform: platform, RoomCode: roomCode, Err: err}
}

// platformOf names the site a room code belongs to, as the room list shows it.
func platformOf(roomCode string) string {
	switch {
	case strings.HasPrefix(roomCode, "bi-"), strings.HasPrefix(roomCode, "BV"), strings.HasPrefix(roomCode, "av"):
		return "B站"
	case strings.HasPrefix(roomCode, "do-"):
		return "斗鱼"
	case strings.HasPrefix(roomCode, "hu-"):
		return "虎牙"
	case strings.HasPrefix(roomCode, "yt-"), strings.HasPrefix(roomCode, "ytv-"):
		return "YouTube"
	case strings.HasPrefix(roomCode, "tw-"):
		return "Twitch"
	case strings.HasPrefix(roomCode, "dy-"):
		return "抖音"
	default:
		return ""
	}
}
