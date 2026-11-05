package danmaku

import (
	"testing"
)

// Live test against douyin: connects to the webcast im push wss endpoint and
// requires at least one chat message. Override with DOUYIN_ROOM (public web
// rid from live.douyin.com/{rid}) when the defaults go offline.
func TestDouyinDanmakuLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	runLiveDanmaku(t, "DOUYIN_ROOM", "548507406983", []string{"85516215620", "775472915676"},
		func(room string) liveDanmakuClient { return NewDouyinDanmakuClient(room) })
}

// A frame whose length prefix fits the pre-prefix slice but not the payload
// used to panic instead of returning an error, taking the whole process down.
func TestPbParseTruncatedLength(t *testing.T) {
	data := []byte{0x0a, 0x02, 0xff} // tag, length 2, only one payload byte
	if _, err := pbParse(data); err == nil {
		t.Fatal("expected a truncation error")
	}
}
