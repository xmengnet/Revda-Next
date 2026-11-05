package dmlive

import (
	"strings"
	"testing"

	"revda/internal/engine/ffmpegctl"
)

func TestDetectSite(t *testing.T) {
	cases := []struct {
		url  string
		site Site
		page int
	}{
		{"https://live.bilibili.com/123", SiteBiliLive, 0},
		{"https://live.bilibili.com/6", SiteBiliLive, 0},
		{"https://www.bilibili.com/video/BV1xx411c7mD", SiteBiliVideo, 0},
		{"https://www.bilibili.com/video/BV1xx411c7mD?p=3", SiteBiliVideo, 3},
		{"https://www.bilibili.com/bangumi/play/ss12345", SiteBiliVideo, 0},
		{"https://www.bilibili.com/bangumi/play/ep999?p=2", SiteBiliVideo, 2},
		{"https://ani.gamer.com.tw/animeVideo.php?sn=12345&p=4", SiteBahaVideo, 4},
		{"https://www.douyu.com/9999", SiteDouyuLive, 0},
		{"https://www.huya.com/12345", SiteHuyaLive, 0},
		{"https://www.twitch.tv/somechannel", SiteTwitchLive, 0},
		{"https://www.youtube.com/@handle/live", SiteYoutubeLive, 0},
		{"dmlive://live.bilibili.com/42", SiteBiliLive, 0},
	}
	for _, c := range cases {
		site, page, err := DetectSite(c.url)
		if err != nil {
			t.Errorf("DetectSite(%q): %v", c.url, err)
			continue
		}
		if site != c.site || page != c.page {
			t.Errorf("DetectSite(%q) = (%d, %d), want (%d, %d)", c.url, site, page, c.site, c.page)
		}
	}
	if _, _, err := DetectSite("https://example.com/nope"); err == nil {
		t.Error("unknown url must error")
	}
}

func TestParseDmlCmd(t *testing.T) {
	c := parseDmlCmd("dml:fsup")
	if !c.fsup {
		t.Fatal("fsup not parsed")
	}
	c = parseDmlCmd("dml:fs=1.5,fa=0.3,speed=6000")
	if c.fs == nil || *c.fs != 1.5 || c.fa == nil || *c.fa != 0.3 || c.speed == nil || *c.speed != 6000 {
		t.Fatalf("key=value parsing failed: %+v", c)
	}
	c = parseDmlCmd("dml:p=7")
	if c.page == nil || *c.page != 7 {
		t.Fatalf("page parsing failed: %+v", c)
	}
	c = parseDmlCmd("dml:r,next,nick")
	if !c.restart || !c.next || !c.nick {
		t.Fatalf("flag parsing failed: %+v", c)
	}
	if parseDmlCmd("other").restart {
		t.Fatal("non-dml prefix must yield empty cmd")
	}
}

func TestWithPage(t *testing.T) {
	if got := withPage("https://www.bilibili.com/bangumi/play/ss123?p=2", 5); !strings.HasSuffix(got, "p=5") {
		t.Fatalf("withPage = %q", got)
	}
	if got := withPage("https://www.bilibili.com/video/BV1xx", 0); strings.Contains(got, "p=") {
		t.Fatalf("page 0 must drop the param: %q", got)
	}
}

func TestTruncateTitleAndFilename(t *testing.T) {
	long := strings.Repeat("弹", 80)
	if got := truncateTitle(long); len([]rune(got)) != 70 {
		t.Fatalf("truncateTitle len = %d, want 70", len([]rune(got)))
	}
	if got := truncateTitle("a/b\\c"); got != "a-b\\c" { // Rust only replaces '/'
		t.Fatalf("separator sanitize = %q", got)
	}
	name := recordFilename("测试标题", "mkv")
	if !strings.HasSuffix(name, ".mkv") || !strings.Contains(name, " - ") {
		t.Fatalf("recordFilename = %q", name)
	}
	if strings.ContainsAny(name, "/") {
		t.Fatalf("filename must not contain path separators: %q", name)
	}
}

func TestDetectStreamType(t *testing.T) {
	if got := detectStreamType(SiteBiliVideo, "https://x"); got != ffmpegctl.TDASH {
		t.Fatalf("bili video must be DASH, got %v", got)
	}
	if got := detectStreamType(SiteBiliLive, "https://x/y.m3u8"); got != ffmpegctl.THLS1 {
		t.Fatalf("bili m3u8 must be HLS1, got %v", got)
	}
	if got := detectStreamType(SiteDouyuLive, "https://x/y.m3u8"); got != ffmpegctl.THLS0 {
		t.Fatalf("douyu m3u8 must be HLS0, got %v", got)
	}
	if got := detectStreamType(SiteHuyaLive, "https://x/y.flv"); got != ffmpegctl.TFLV {
		t.Fatalf("flv must be TFLV, got %v", got)
	}
	// YouTube live is m3u8 but split into video and audio playlists, so it
	// must not take the single-input HLS path.
	if got := detectStreamType(SiteYoutubeLive, "https://x/y.m3u8"); got != ffmpegctl.TDASH {
		t.Fatalf("youtube m3u8 must be DASH (dual input), got %v", got)
	}
}

func TestLastPathSeg(t *testing.T) {
	if got := lastPathSeg("https://www.huya.com/12345"); got != "12345" {
		t.Fatalf("lastPathSeg = %q", got)
	}
	if got := lastPathSeg("https://www.twitch.tv/somechannel/"); got != "somechannel" {
		t.Fatalf("trailing slash mishandled: %q", got)
	}
}
