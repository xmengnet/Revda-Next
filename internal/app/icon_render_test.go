package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestIconRendersInHeadlessView checks mygo actually paints each embedded
// platform mark, not just parses it: a frame of blank boxes paints no colored
// pixels where the icons sit.
func TestIconRendersInHeadlessView(t *testing.T) {
	codes := []string{"bi-545342", "do-475252", "hu-660118", "yt-name", "tw-name", "ep-123", "BV1xx"}

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(8).Gap(8).Children(func() {
			for _, code := range codes {
				ui.Box(c.Key("card_" + code)).Height(60).Children(func() {
					src := platformIcon(code)
					if src == nil {
						t.Errorf("no icon for %q", code)
						return
					}
					switch {
					case src.svg != nil:
						ui.Image(c, src.svg).Size(16, 16)
					case src.bitmap != nil:
						ui.Image(c, src.bitmap).Size(16, 16).Fit(ui.Contain)
					default:
						t.Errorf("icon for %q has no artwork", code)
					}
				})
			}
		})
	}, 400, 600)

	tt.Frame()
	img := tt.Image()
	if img == nil {
		t.Fatal("no rendered image")
	}

	// Each card's icon area must hold painted pixels.
	for i, code := range codes {
		// Cards stack at y = 8 + i*68; the icon sits inside the card.
		y0 := 8 + i*68
		filled := 0
		for y := y0 + 10; y < y0+50; y++ {
			for x := 10; x < 40; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
					filled++
				}
			}
		}
		if filled == 0 {
			t.Errorf("no painted pixels in the icon area for %q", code)
		}
	}
}
