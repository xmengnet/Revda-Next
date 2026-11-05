package danmaku

import (
	"strings"
	"unicode"
)

// CleanDanmakuText removes what libass cannot draw (colour emoji, dingbats,
// private-use and format characters) so they show as tofu boxes — Douyu chat
// is full of them — and collapses whitespace. It also caps the length, which
// keeps an absurd message from owning a lane for a long time.
func CleanDanmakuText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r),
			unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r),
			unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			// Control, format (ZWJ, bidi), private-use and separators.
		case r == 0x20E3: // combining enclosing keycap
		case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF: // variation selectors
		case r >= 0x1F000 && r <= 0x1FAFF: // emoji and pictographs
		case r >= 0x2300 && r <= 0x23FF: // technical symbols used as emoji
		case r >= 0x2600 && r <= 0x27BF: // misc symbols and dingbats
		case r >= 0x2B00 && r <= 0x2BFF: // misc symbols and arrows used as emoji
		case r == 0x3030, r == 0x303D, r == 0x3297, r == 0x3299:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	return truncateRunes(out, 300)
}

func truncateRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}
