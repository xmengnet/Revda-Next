package danmaku

import (
	"bytes"
	"testing"
)

// Golden vectors from dmlive/tars-stream's tars_encoder.rs tests.
func TestTarsEncodeGolden(t *testing.T) {
	tests := []struct {
		name  string
		build func(e *tarsEncoder)
		want  []byte
	}{
		{
			name: "int8 minimal/zero/extended tag",
			build: func(e *tarsEncoder) {
				e.writeInt8(0, -127)
				e.writeInt8(14, 127)
				e.writeInt8(255, -1)
				e.writeInt8(3, 0)
			},
			want: []byte{0x00, 0x81, 0xe0, 0x7f, 0xf0, 0xff, 0xff, 0x3c},
		},
		{
			name: "int32 narrowing",
			build: func(e *tarsEncoder) {
				e.writeInt32(0, 90909)
				e.writeInt32(15, 255)
				e.writeInt32(14, -127)
				e.writeInt32(14, -95234)
			},
			want: []byte{
				0x02, 0x00, 0x01, 0x63, 0x1d, 0xf1, 0x0f, 0x00,
				0xff, 0xe0, 0x81, 0xe2, 0xff, 0xfe, 0x8b, 0xfe,
			},
		},
		{
			name: "string1",
			build: func(e *tarsEncoder) {
				e.writeString(0, "hello wrold!")
			},
			want: append([]byte{0x06, 0x0c}, "hello wrold!"...),
		},
		{
			name: "bytes with orphan head quirk",
			build: func(e *tarsEncoder) {
				e.writeBytes(9, []byte("hello world!"))
			},
			want: []byte{0x9d, 0x00, 0x00, 0x0c, 'h', 'e', 'l', 'l', 'o', ' ', 'w', 'o', 'r', 'l', 'd', '!'},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e tarsEncoder
			tt.build(&e)
			if !bytes.Equal(e.buf, tt.want) {
				t.Fatalf("encoded %x, want %x", e.buf, tt.want)
			}
		})
	}
}

// The heartbeat must parse with our decoder (tWsCmd=3 + 105-byte payload) —
// it is the real wire format huya speaks.
func TestTarsDecodeHeartbeat(t *testing.T) {
	d := newTarsDecoder(huyaHeartbeat)
	cmd, err := d.readInt(0)
	if err != nil || cmd != 3 {
		t.Fatalf("cmd = %d, %v; want 3", cmd, err)
	}
	payload, err := d.readBytes(1)
	if err != nil {
		t.Fatalf("readBytes: %v", err)
	}
	if len(payload) != 105 {
		t.Fatalf("payload len = %d, want 105", len(payload))
	}
}

func TestTarsBytesRoundTrip(t *testing.T) {
	payload := make([]byte, 300) // size > 255 exercises INT16 size encoding
	for i := range payload {
		payload[i] = byte(i)
	}
	var e tarsEncoder
	e.writeBytes(2, payload)

	d := newTarsDecoder(e.buf)
	got, err := d.readBytes(2)
	if err != nil {
		t.Fatalf("readBytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch")
	}
}

func TestBuildHuyaRegister(t *testing.T) {
	got := buildHuyaRegister(12345, 111, 222)
	want := []byte{
		0x00, 0x01, // iCmdType = EWSCmd_RegisterReq
		0x1d, 0x00, 0x00, 0x10, // simple list payload, 16 bytes
		0x01, 0x30, 0x39, // lUid = 12345
		0x10, 0x01, // bAnonymous = true
		0x26, 0x00, // sGuid = ""
		0x36, 0x00, // sToken = ""
		0x40, 0x6f, // lTid = 111
		0x51, 0x00, 0xde, // lSid = 222 (int16: does not fit int8)
		0x6c, // lGroupId = 0 (zero tag)
		0x7c, // lGroupType = 0 (zero tag)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("register packet %x, want %x", got, want)
	}
}

// buildFakeChatPush assembles a 1400 chat push the way the huya server does,
// to exercise the full decodeMsg chain offline.
func buildFakeChatPush(nick, text string, color int32) []byte {
	var body tarsEncoder
	body.putHead(0, tarsTypeStructBegin) // sender struct
	body.writeInt32(0, 12345)            // uid
	body.writeInt32(1, 67890)            // imid
	body.writeString(2, nick)
	body.writeInt32(3, 1) // gender
	body.putHead(0, tarsTypeStructEnd)
	body.writeString(3, text)
	body.putHead(6, tarsTypeStructBegin) // danmaku struct
	body.writeInt32(0, color)
	body.putHead(0, tarsTypeStructEnd)

	var payload tarsEncoder
	payload.writeInt32(1, 1400)
	payload.writeBytes(2, body.buf)

	var outer tarsEncoder
	outer.writeInt32(0, 7)
	outer.writeBytes(1, payload.buf)
	return outer.buf
}

func TestDecodeMsgFakeChatPush(t *testing.T) {
	c := NewHuyaDanmakuClient("660118")
	msg := c.decodeMsg(buildFakeChatPush("测试用户", "  你好世界  ", 0xff00ff))
	if msg == nil {
		t.Fatal("no message decoded")
	}
	if msg.Nick != "测试用户" || msg.Text != "你好世界" {
		t.Fatalf("got [%s] %q", msg.Nick, msg.Text)
	}
	if msg.Color != "ff00ff" {
		t.Fatalf("color = %s, want ff00ff", msg.Color)
	}

	// non-positive color falls back to white
	msg = c.decodeMsg(buildFakeChatPush("a", "hi", 0))
	if msg == nil || msg.Color != "ffffff" {
		t.Fatalf("color fallback failed: %+v", msg)
	}

	// tWsCmd != 7 (e.g. register ack) is skipped
	if c.decodeMsg([]byte{0x00, 0x01}) != nil {
		t.Fatal("non-push frame should be skipped")
	}

	// small non-zero color keeps its value
	msg = c.decodeMsg(buildFakeChatPush("a", "hi", 1))
	if msg == nil || msg.Color != "000001" {
		t.Fatalf("color = %+v, want 000001", msg)
	}
}

func TestExtractHuyaIDs(t *testing.T) {
	page := `{"roomProfile":{"lUid":1541541294},"roomInfo":{"tProfileInfo":` +
		`{"lUid":1541541294,"lYyid":1735594525,"sNick":"x"},` +
		`"tLiveInfo":{"lChannelId":1541541294,"tLiveStreamInfo":` +
		`{"vStreamInfo":{"value":[{"lSubChannelId":1541541294}]}}}}}`
	ayyuid, topsid, subsid, err := extractHuyaIDs(page)
	if err != nil || ayyuid != 1735594525 || topsid != 1541541294 || subsid != 1541541294 {
		t.Fatalf("ids = %d/%d/%d, %v", ayyuid, topsid, subsid, err)
	}

	if _, _, _, err := extractHuyaIDs("no ids here"); err == nil {
		t.Fatal("expected error for page without ids")
	}
}
