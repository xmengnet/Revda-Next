package app

import (
	_ "embed"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Platform marks for the room cards, from each platform's own artwork:
// bilibili, YouTube and Twitch come from simple-icons (official brand marks,
// single-color paths, so a card can tint them); douyu and huya are the logos
// their own sites serve, kept in their brand colors.
//
// douyu is the favicon its site ships; huya is the tiger head cropped out of
// the horizontal logo on huya.com, so the mark reads at the 22px a card shows
// it at.

//go:embed assets/bilibili.svg
var iconBilibiliSVG []byte

//go:embed assets/youtube.svg
var iconYoutubeSVG []byte

//go:embed assets/twitch.svg
var iconTwitchSVG []byte

//go:embed assets/douyu-icon.png
var iconDouyuPNG []byte

//go:embed assets/huya-icon.png
var iconHuyaPNG []byte

var (
	iconBilibili = ui.MustParseSVG(iconBilibiliSVG)
	iconYoutube  = ui.MustParseSVG(iconYoutubeSVG)
	iconTwitch   = ui.MustParseSVG(iconTwitchSVG)
)

// platformIconSource is what a card shows for a room code: either a tinted
// official SVG or the platform's own bitmap.
type platformIconSource struct {
	svg    *ui.SVG
	bitmap *ui.Bitmap
}

// loadPlatformIconBitmap decodes an embedded platform PNG. It returns nil
// when the bytes are not a bitmap the UI can show.
func loadPlatformIconBitmap(data []byte) *ui.Bitmap {
	if bmp, err := ui.DecodeBitmap(data); err == nil {
		return bmp
	}
	return nil
}

// platformIcon picks the mark for a room code, or nil when the code names no
// known platform. Bangumi codes (ep-/ss-) are bilibili content.
func platformIcon(code string) *platformIconSource {
	switch {
	case hasAnyPrefix(code, "bi-", "BV", "av", "ep-", "ss-"):
		return &platformIconSource{svg: iconBilibili}
	case hasAnyPrefix(code, "do-"):
		return &platformIconSource{bitmap: douyuBitmap}
	case hasAnyPrefix(code, "hu-"):
		return &platformIconSource{bitmap: huyaBitmap}
	case hasAnyPrefix(code, "yt-", "ytv-"):
		return &platformIconSource{svg: iconYoutube}
	case hasAnyPrefix(code, "tw-"):
		return &platformIconSource{svg: iconTwitch}
	default:
		return nil
	}
}

var (
	douyuBitmap = loadPlatformIconBitmap(iconDouyuPNG)
	huyaBitmap  = loadPlatformIconBitmap(iconHuyaPNG)
)

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
