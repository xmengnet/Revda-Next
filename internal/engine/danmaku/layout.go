package danmaku

import (
	"math"
	"strings"
)

// The ASS canvas is always 1920x1080; the settings scale into it.
const danmakuCanvasHeight = 1080.0

// clampDanmakuArea keeps the configured coverage in a sane range. 50 keeps
// the engine's historical top-half layout, which was 540/1080.
func clampDanmakuArea(pct int) int {
	switch {
	case pct <= 0:
		return 50
	case pct < 10:
		return 10
	case pct > 100:
		return 100
	}
	return pct
}

// anchorIndex maps the config value to 0 top, 1 middle, 2 bottom.
func anchorIndex(anchor string) int {
	switch strings.ToLower(strings.TrimSpace(anchor)) {
	case "middle", "center", "centre":
		return 1
	case "bottom":
		return 2
	}
	return 0
}

func anchorName(i int) string {
	switch i {
	case 1:
		return "middle"
	case 2:
		return "bottom"
	}
	return "top"
}

// danmakuChannelNum is the number of lanes the covered area holds.
func danmakuChannelNum(fontSize, areaPct int) int {
	if fontSize <= 0 {
		fontSize = 40
	}
	area := danmakuCanvasHeight * float64(clampDanmakuArea(areaPct)) / 100
	n := int(math.Ceil(area / float64(fontSize)))
	if n < 1 {
		n = 1
	}
	return n
}

// danmakuBaseY is the top of the covered area for the chosen anchor.
func danmakuBaseY(areaPct, anchor int) int {
	area := danmakuCanvasHeight * float64(clampDanmakuArea(areaPct)) / 100
	switch anchor {
	case 1:
		return int(math.Round((danmakuCanvasHeight - area) / 2))
	case 2:
		return int(math.Round(danmakuCanvasHeight - area))
	}
	return 0
}
