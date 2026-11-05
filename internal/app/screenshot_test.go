package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"revda/internal/imagecache"
	"revda/internal/sites"

	"github.com/egoist/mygo/ui"
)

// shotRooms are invented rooms: the codes, titles and owners are all made up,
// so a README screenshot never shows the user's real saved rooms, history or
// cookies.
var shotRooms = []struct {
	code, title, owner string
	living             bool
}{
	{"bi-100001", "深夜电台：一起听点歌", "星河电台", true},
	{"do-200002", "午后杂谈与手冲咖啡", "南风客厅", true},
	{"hu-300003", "像素冒险通关中", "像素游戏厅", true},
	{"yt-demo", "late night lofi mix", "ambient room", true},
	{"tw-demo", "writing a tiny renderer", "code and coffee", true},
	{"dy-400004", "雨天爵士唱片推荐", "北岸唱片行", true},
	{"bi-100005", "周末拆解老式合成器", "电压实验室", false},
	{"hu-300006", "城市夜景散步直播", "午夜散步者", true},
}

var coverStops = [][2]color.RGBA{
	{{0x2b, 0x58, 0x76, 0xff}, {0x4e, 0x43, 0x76, 0xff}},
	{{0x13, 0x4e, 0x5e, 0xff}, {0x71, 0xb2, 0x80, 0xff}},
	{{0x41, 0x29, 0x5a, 0xff}, {0x2f, 0x07, 0x43, 0xff}},
	{{0x35, 0x5c, 0x7d, 0xff}, {0x6c, 0x5b, 0x7b, 0xff}},
	{{0x14, 0x1e, 0x30, 0xff}, {0x24, 0x3b, 0x55, 0xff}},
	{{0x3a, 0x1c, 0x71, 0xff}, {0x85, 0x5c, 0x8c, 0xff}},
	{{0x0f, 0x20, 0x27, 0xff}, {0x2c, 0x53, 0x64, 0xff}},
	{{0x42, 0x21, 0x3a, 0xff}, {0x7a, 0x50, 0x6b, 0xff}},
}

// makeCover paints a synthetic cover: a diagonal gradient with soft bands.
// No text and no real artwork, so nothing in it can identify anyone.
func makeCover(seed int) []byte {
	const w, h = 500, 260
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	from, to := coverStops[seed%len(coverStops)][0], coverStops[seed%len(coverStops)][1]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := (float64(x)/w*0.65 + float64(y)/h*0.35)
			c := color.RGBA{
				R: uint8(float64(from.R) + (float64(to.R)-float64(from.R))*t),
				G: uint8(float64(from.G) + (float64(to.G)-float64(from.G))*t),
				B: uint8(float64(from.B) + (float64(to.B)-float64(from.B))*t),
				A: 0xff,
			}
			// Slight horizontal banding keeps flat gradients from looking
			// like a solid fill at thumbnail size.
			if (y/18)%2 == 0 {
				c.R = uint8(min(255, int(c.R)+6))
				c.G = uint8(min(255, int(c.G)+6))
				c.B = uint8(min(255, int(c.B)+6))
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// TestGenerateScreenshots renders the app with MyGo's headless tester and
// writes the README screenshots. It is opt-in so ordinary test runs do not
// touch the repository:
//
//	REVDA_SCREENSHOTS=1 go test -run TestGenerateScreenshots ./internal/app
func TestGenerateScreenshots(t *testing.T) {
	if os.Getenv("REVDA_SCREENSHOTS") == "" {
		t.Skip("set REVDA_SCREENSHOTS=1 to regenerate pictures/revda-*.png")
	}

	// Isolate configuration in case anything here touches it, and construct
	// the App directly: New() applies the theme through mygo's main loop,
	// which only runs inside a real app. Direct construction also guarantees
	// that no saved rooms, history, cookies or record path are read.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	a := &App{
		roomInfos:          make(map[string]*sites.RoomInfo),
		loading:            make(map[string]bool),
		currentTheme:       "dark",
		settingFontScale:   1.0,
		settingFontOpacity: 1.0,
		settingSpeed:       8,
		imgCache:           imagecache.New(nil),
	}

	covers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var seed int
		_, _ = fmt.Sscanf(r.URL.Path, "/cover%d", &seed)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makeCover(seed))
	}))
	defer covers.Close()

	a.savedRooms = nil
	a.roomInfos = make(map[string]*sites.RoomInfo, len(shotRooms))
	for i, r := range shotRooms {
		a.savedRooms = append(a.savedRooms, r.code)
		a.roomInfos[r.code] = &sites.RoomInfo{
			RoomCode: r.code,
			Title:    r.title,
			Owner:    r.owner,
			IsLiving: r.living,
			Cover:    coversURL(covers.URL, i),
		}
	}
	a.loading = make(map[string]bool)

	// The card covers load asynchronously; wait for them so the screenshot
	// shows artwork instead of spinners.
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending := false
		for _, info := range a.roomInfos {
			if a.imgCache.Get(info.Cover) == nil {
				pending = true
			}
		}
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cover cache did not warm up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	outDir := filepath.Join("..", "..", "pictures")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir pictures: %v", err)
	}

	writeShot(t, a, outDir, "revda-main-dark.png", true, false)
	writeShot(t, a, outDir, "revda-main-light.png", false, false)
	// Settings: the modal is what the "设置" button opens.
	a.showSettings = true
	writeShot(t, a, outDir, "revda-settings-dark.png", true, true)
}

func coversURL(base string, seed int) string {
	return fmt.Sprintf("%s/cover%d", base, seed)
}

func writeShot(t *testing.T, a *App, dir, name string, dark, settleModal bool) {
	t.Helper()
	const w, h = 1180, 680
	tt := ui.NewTester(a.View, w, h)
	tt.SetDark(dark)
	if settleModal {
		tt.Advance(400 * time.Millisecond) // finish the modal's entrance
	}
	tt.Frame()
	img := tt.Image()
	if img == nil {
		t.Fatalf("%s: tester produced no image", name)
	}

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("%s: encode: %v", name, err)
	}
	t.Logf("wrote %s (%dx%d)", path, img.Bounds().Dx(), img.Bounds().Dy())
}
