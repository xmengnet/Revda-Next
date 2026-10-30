package ass

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

const Header = `[Script Info]
Title: Danmaku file
ScriptType: v4.00+
WrapStyle: 0
ScaledBorderAndShadow: yes
PlayResX: 1920
PlayResY: 1080

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Sans,%d,&H%02XFFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,7,0,0,0,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`

type ChannelAllocator struct {
	mu           sync.Mutex
	numChannels  int
	channelY     []int
	channelFree  []time.Time
	fontSize     int
	fontAlpha    float64
	danmakuSpeed int // seconds across screen
}

func NewChannelAllocator(fontSize int, fontAlpha float64, speedSec int) *ChannelAllocator {
	if fontSize <= 0 {
		fontSize = 40
	}
	if speedSec <= 0 {
		speedSec = 8
	}
	numChannels := 1080 / (fontSize + 12)
	channelY := make([]int, numChannels)
	for i := 0; i < numChannels; i++ {
		channelY[i] = (i + 1) * (fontSize + 10)
	}
	return &ChannelAllocator{
		numChannels:  numChannels,
		channelY:     channelY,
		channelFree:  make([]time.Time, numChannels),
		fontSize:     fontSize,
		fontAlpha:    fontAlpha,
		danmakuSpeed: speedSec,
	}
}

func (a *ChannelAllocator) FormatDialogue(now time.Time, startPTS time.Duration, text string, colorHex string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Find the first channel that is free
	chosen := -1
	for i := 0; i < a.numChannels; i++ {
		if now.After(a.channelFree[i]) {
			chosen = i
			break
		}
	}
	if chosen == -1 {
		// All channels busy, randomly pick one in upper half
		chosen = rand.IntN(max(1, a.numChannels/2))
	}

	// Approximate width of text: ~ fontSize * length * 0.9
	textWidth := len([]rune(text)) * a.fontSize
	// Duration before next message can enter without collision
	safeDelay := time.Duration(float64(a.danmakuSpeed)*(float64(textWidth)/1920.0+0.15)) * time.Second
	a.channelFree[chosen] = now.Add(safeDelay)

	y := a.channelY[chosen]
	startSec := startPTS.Seconds()
	endSec := startSec + float64(a.danmakuSpeed)

	startStr := formatASSTime(startSec)
	endStr := formatASSTime(endSec)

	// Move effect from X=1920 to X=-(textWidth+50)
	endX := -(textWidth + 50)
	colorTag := ""
	if colorHex != "" && colorHex != "FFFFFF" {
		colorTag = fmt.Sprintf("{\\c&H%s&}", colorHex)
	}

	return fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,{\\move(1920,%d,%d,%d)}%s%s\n",
		startStr, endStr, y, endX, y, colorTag, text)
}

func formatASSTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	h := int(sec) / 3600
	m := (int(sec) % 3600) / 60
	s := int(sec) % 60
	cs := int((sec - float64(int(sec))) * 100)
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}
