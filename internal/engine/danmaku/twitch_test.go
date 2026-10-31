package danmaku

import "testing"

// Sample lines shaped after the IRCv3 messages Twitch's chat gateway sends.
func TestDecodeTwitchLine(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		want  *DanmakuMessage
		wantN bool // whether a message is expected
	}{
		{
			name:  "chat message with color",
			line:  "@badge-info=;badges=;color=#1E90FF;display-name=DineshTheGreat;emotes=;first-msg=0;flags=;id=1;mod=0;room-id=123;subscriber=0;tmi-sent-ts=1650000000000;turbo=0;user-id=456;user-type= :dineshthegreat!dineshthegreat@dineshthegreat.tmi.twitch.tv PRIVMSG #dinesh :Hello, world!",
			wantN: true,
			want:  &DanmakuMessage{Nick: "DineshTheGreat", Text: "Hello, world!", Color: "1E90FF", Position: 0},
		},
		{
			name:  "no color defaults to white",
			line:  "@badges=;color=;display-name=Foo;emotes=;id=2 :foo!foo@foo.tmi.twitch.tv PRIVMSG #bar :nice stream",
			wantN: true,
			want:  &DanmakuMessage{Nick: "Foo", Text: "nice stream", Color: "ffffff", Position: 0},
		},
		{
			name:  "colons in text are kept",
			line:  "@color=#00FF00;display-name=Baz;emotes= :baz!baz@baz.tmi.twitch.tv PRIVMSG #qux :time is 12:30:45",
			wantN: true,
			want:  &DanmakuMessage{Nick: "Baz", Text: "time is 12:30:45", Color: "00FF00", Position: 0},
		},
		{
			name:  "trailing CR is trimmed",
			line:  "@color=;display-name=Foo;emotes= :foo!foo@foo.tmi.twitch.tv PRIVMSG #bar :hi\r",
			wantN: true,
			want:  &DanmakuMessage{Nick: "Foo", Text: "hi", Color: "ffffff", Position: 0},
		},
		{name: "JOIN has no display-name", line: ":justinfan12345!justinfan12345@justinfan12345.tmi.twitch.tv JOIN #bar", wantN: false},
		{name: "server PING ignored", line: "PING :tmi.twitch.tv", wantN: false},
		{name: "empty text dropped", line: "@color=;display-name=Foo :foo!foo@foo.tmi.twitch.tv PRIVMSG #bar :", wantN: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeTwitchLine(tt.line)
			if !tt.wantN {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a message, got nil")
			}
			got.Time = tt.want.Time
			if *got != *tt.want {
				t.Errorf("got %+v, want %+v", *got, *tt.want)
			}
		})
	}
}
