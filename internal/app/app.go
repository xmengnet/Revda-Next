package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"revda/internal/config"
	"revda/internal/imagecache"
	"revda/internal/player"
	"revda/internal/sites"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type App struct {
	mu        sync.RWMutex
	win       *mygo.Window
	cfgMgr    *config.Manager
	dmliveCfg *config.DmliveConfigManager
	sitesSvc  *sites.Service
	imgCache  *imagecache.Cache
	gridState ui.GridState

	inputRoom    string
	tabIndex     int // 0: 收藏, 1: 历史
	savedRooms   []string
	historyRooms []string
	roomInfos    map[string]*sites.RoomInfo
	loading      map[string]bool
	refreshing   bool

	// Filters
	platformFilter int    // 0: 全部, 1: B站, 2: 斗鱼, 3: 虎牙, 4: YouTube, 5: Twitch, 6: 抖音
	onlyLiving     bool   // 仅看开播
	searchFilter   string // 过滤主播或标题

	// Settings modal
	showSettings              bool
	currentTheme              string // "system", "dark", "light"
	settingFontScale          float64
	settingFontOpacity        float64
	settingSpeed              float64
	settingBCookie            string
	settingCookiesFromBrowser string
	settingRecordByDefault    bool
	settingRecordPath         string

	statusMsg    string
	statusIsErr  bool
	statusUntil  time.Time // when the status toast hides itself
	statusHideAt time.Time // the frame after statusUntil, the toast stops building
}

func New() (*App, error) {
	cfg, err := config.NewManager()
	if err != nil {
		return nil, fmt.Errorf("init config error: %w", err)
	}

	dmliveCfg, err := config.NewDmliveConfigManager()
	if err != nil {
		return nil, fmt.Errorf("init dmlive config error: %w", err)
	}
	dData := dmliveCfg.Get()

	a := &App{
		cfgMgr:                    cfg,
		dmliveCfg:                 dmliveCfg,
		sitesSvc:                  sites.NewService(),
		roomInfos:                 make(map[string]*sites.RoomInfo),
		loading:                   make(map[string]bool),
		savedRooms:                cfg.GetSaved(),
		historyRooms:              cfg.GetHistory(),
		currentTheme:              cfg.GetTheme(),
		settingFontScale:          dData.FontScale,
		settingFontOpacity:        1.0 - dData.FontAlpha,
		settingSpeed:              float64(dData.DanmakuSpeed) / 1000, // config stores ms; slider is seconds
		settingBCookie:            dData.BCookie,
		settingCookiesFromBrowser: dData.CookiesFromBrowser,
		settingRecordPath:         dData.RecordPath,
	}

	if a.settingFontOpacity < 0.1 || a.settingFontOpacity > 1.0 {
		a.settingFontOpacity = 1.0
	}

	a.applyTheme(a.currentTheme)

	if len(a.savedRooms) == 0 && len(a.historyRooms) > 0 {
		a.tabIndex = 1
	}

	a.imgCache = imagecache.New(func(url string, bmp *ui.Bitmap) {
		if a.win != nil {
			a.win.Update(func() {
				// Bitmap ready, repaint
			})
		}
	})

	return a, nil
}

func (a *App) SetWindow(w *mygo.Window) {
	a.win = w
}

func (a *App) Init() {
	a.applyTheme(a.currentTheme)
	a.Refresh()
}

func (a *App) applyTheme(th string) {
	switch th {
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) SetTheme(th string) {
	a.mu.Lock()
	a.currentTheme = th
	a.mu.Unlock()
	a.applyTheme(th)
	_ = a.cfgMgr.SetTheme(th)
	if a.win != nil {
		a.win.Invalidate()
	}
}

// statusDuration is how long a status toast stays before it hides itself.
// statusExitDuration is how long its exit animation runs after that.
const (
	statusDuration     = 4 * time.Second
	statusExitDuration = 200 * time.Millisecond
)

func (a *App) SetStatus(msg string, isErr bool) {
	a.mu.Lock()
	a.statusMsg = msg
	a.statusIsErr = isErr
	a.statusUntil = time.Now().Add(statusDuration)
	a.statusHideAt = time.Time{}
	a.mu.Unlock()
	if a.win != nil {
		a.win.Update(func() {})
	}
}

// DismissStatus hides the status toast at once, as its close button does.
func (a *App) DismissStatus() {
	a.mu.Lock()
	a.statusMsg = ""
	a.statusUntil = time.Time{}
	a.statusHideAt = time.Time{}
	a.mu.Unlock()
}

// statusSnapshot reads the message the toast shows, after expireStatus has
// decided its fate for this frame.
func (a *App) statusSnapshot() (msg string, isErr bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.statusMsg, a.statusIsErr
}

// expireStatus moves a toast whose time is up into its exit animation, and
// drops it once that has run. It reports the toast's state after the move:
// whether it still has a message to show, and when the next frame is due.
func (a *App) expireStatus(now time.Time) (showing bool, next time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Fresh: nothing to expire yet. A toast in its exit animation keeps
	// showing until the animation has had its time.
	if !a.statusUntil.IsZero() && now.Before(a.statusUntil) {
		return a.statusMsg != "", a.statusUntil
	}

	// Past its time: stop building it this frame so the exit animation can
	// play, and drop it once that is done.
	if a.statusHideAt.IsZero() {
		a.statusHideAt = now
	}
	a.statusUntil = time.Time{}
	if now.Sub(a.statusHideAt) < statusExitDuration {
		return a.statusMsg != "", a.statusHideAt.Add(statusExitDuration)
	}

	a.statusMsg = ""
	a.statusHideAt = time.Time{}
	return false, time.Time{}
}

func (a *App) Refresh() {
	a.mu.Lock()
	a.refreshing = true
	a.savedRooms = a.cfgMgr.GetSaved()
	a.historyRooms = a.cfgMgr.GetHistory()
	allRooms := make(map[string]struct{})
	for _, r := range a.savedRooms {
		allRooms[r] = struct{}{}
		a.loading[r] = true
	}
	for _, r := range a.historyRooms {
		allRooms[r] = struct{}{}
		a.loading[r] = true
	}
	a.mu.Unlock()

	go func() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 6)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		for code := range allRooms {
			wg.Add(1)
			sem <- struct{}{}
			go func(c string) {
				defer wg.Done()
				defer func() { <-sem }()

				info, err := a.sitesSvc.GetRoomInfo(ctx, c)
				if err == nil && info != nil {
					if info.Cover != "" {
						_ = a.imgCache.Get(info.Cover)
					}
					a.mu.Lock()
					a.roomInfos[c] = info
					a.loading[c] = false
					a.mu.Unlock()
				} else {
					a.mu.Lock()
					a.loading[c] = false
					a.mu.Unlock()
				}

				if a.win != nil {
					a.win.Update(func() {
						a.sortSavedRooms()
					})
				}
			}(code)
		}

		wg.Wait()
		a.mu.Lock()
		a.refreshing = false
		a.mu.Unlock()

		if a.win != nil {
			a.win.Update(func() {
				a.sortSavedRooms()
			})
		}
	}()
}

func (a *App) sortSavedRooms() {
	a.mu.Lock()
	defer a.mu.Unlock()
	sort.SliceStable(a.savedRooms, func(i, j int) bool {
		r1 := a.savedRooms[i]
		r2 := a.savedRooms[j]
		info1 := a.roomInfos[r1]
		info2 := a.roomInfos[r2]
		live1 := info1 != nil && info1.IsLiving
		live2 := info2 != nil && info2.IsLiving
		if live1 && !live2 {
			return true
		}
		if !live1 && live2 {
			return false
		}
		return false
	})
}

func (a *App) Open(input string) {
	// Go native engine handles everything the app can parse.
	input = strings.TrimSpace(input)
	if code, _, err := sites.NormalizeInput(input); err == nil && player.CanResolveDirectly(code) {
		a.OpenDirect(code, a.settingRecordByDefault)
		return
	}
	a.SetStatus(fmt.Sprintf("不支持的输入: %s", input), true)
}

func (a *App) OpenDirect(code string, record bool) {
	// LaunchDirectWithConfig blocks on stream resolution, so run it in the
	// background — calling it from a UI event freezes the whole window until
	// mpv appears.
	a.SetStatus(fmt.Sprintf("正在解析并拉起 Go 原生播放: %s ...", code), false)
	go func() {
		// Videos and bangumi have no "live" state to check; a live room that
		// is not streaming is caught here before the engine reports it as a
		// technical resolution failure.
		if !isVideoCode(code) {
			if msg, ok := a.offlineNotice(code); ok {
				a.SetStatus(msg, true)
				return
			}
		}
		dData := a.dmliveCfg.Get()
		if err := player.LaunchDirectWithConfigRecord(code, dData, record); err != nil {
			a.SetStatus(playFailureMessage(err, code), true)
			return
		}
		_ = a.cfgMgr.AddHistory(code)
		a.mu.Lock()
		a.historyRooms = a.cfgMgr.GetHistory()
		a.mu.Unlock()
		msg := "已通过 Go 原生引擎极速拉起播放"
		if record {
			msg = "已启动播放并录制(含弹幕字幕轨)"
		}
		a.SetStatus(fmt.Sprintf("%s: %s", msg, code), false)
	}()
}

// isVideoCode reports whether code is a video or bangumi entry, which is
// always playable and so has no live state to pre-check.
func isVideoCode(code string) bool {
	return strings.HasPrefix(code, "BV") ||
		strings.HasPrefix(code, "av") ||
		strings.HasPrefix(code, "ep") ||
		strings.HasPrefix(code, "ss") ||
		strings.HasPrefix(code, "ytv-")
}

// offlineNotice checks a live room's state before playback is attempted. It
// returns a message and true when the room is known not to be live; false
// when the state is unknown or the room is live, in which case playback
// proceeds and the engine reports any failure.
func (a *App) offlineNotice(code string) (string, bool) {
	// A room the list already holds info for needs no request: the card the
	// user clicked just showed its live state.
	a.mu.RLock()
	info := a.roomInfos[code]
	a.mu.RUnlock()
	if info != nil {
		if !info.IsLiving {
			return fmt.Sprintf("%s 未开播，主播当前没有直播", code), true
		}
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	info, err := a.sitesSvc.GetRoomInfo(ctx, code)
	if err != nil {
		// State unknown — let the engine try and report what it finds.
		return "", false
	}
	if !info.IsLiving {
		return fmt.Sprintf("%s 未开播，主播当前没有直播", code), true
	}
	return "", false
}

// playFailureMessage turns an engine error into one sentence naming the room
// and the cause, instead of the raw technical error.
func playFailureMessage(err error, code string) string {
	var playErr *player.PlayError
	if errors.As(err, &playErr) {
		return playErr.Message()
	}
	// An error that never went through wrapPlayError — classify it here so a
	// streamer being offline still reads as such.
	kind := player.ClassifyError(err)
	if kind != player.ErrUnknown {
		return (&player.PlayError{Kind: kind, RoomCode: code, Err: err}).Message()
	}
	return fmt.Sprintf("播放 %s 失败：%v", code, err)
}

func (a *App) OpenWithRecord(input string) {
	input = strings.TrimSpace(input)
	code, _, err := sites.NormalizeInput(input)
	if err != nil {
		a.SetStatus(fmt.Sprintf("解析错误: %v", err), true)
		return
	}
	a.OpenDirect(code, true)
}

func (a *App) Save(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		a.SetStatus("请输入直播间代码或网址", true)
		return
	}

	code, _, err := sites.NormalizeInput(input)
	if err != nil {
		a.SetStatus(fmt.Sprintf("解析错误: %v", err), true)
		return
	}

	if err := a.cfgMgr.AddSaved(code); err != nil {
		a.SetStatus(fmt.Sprintf("收藏失败: %v", err), true)
		return
	}

	a.mu.Lock()
	a.savedRooms = a.cfgMgr.GetSaved()
	a.loading[code] = true
	a.mu.Unlock()

	a.SetStatus(fmt.Sprintf("已收藏: %s", code), false)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		info, err := a.sitesSvc.GetRoomInfo(ctx, code)
		if err == nil && info != nil {
			if info.Cover != "" {
				_ = a.imgCache.Get(info.Cover)
			}
			a.mu.Lock()
			a.roomInfos[code] = info
			a.loading[code] = false
			a.mu.Unlock()
			if a.win != nil {
				a.win.Update(func() {
					a.sortSavedRooms()
				})
			}
		}
	}()
}

func (a *App) RemoveSaved(code string) {
	if err := a.cfgMgr.RemoveSaved(code); err != nil {
		a.SetStatus(fmt.Sprintf("移除收藏失败: %v", err), true)
		return
	}
	a.mu.Lock()
	a.savedRooms = a.cfgMgr.GetSaved()
	a.mu.Unlock()
	a.SetStatus(fmt.Sprintf("已取消收藏: %s", code), false)
}

func (a *App) RemoveHistory(code string) {
	if err := a.cfgMgr.RemoveHistory(code); err != nil {
		a.SetStatus(fmt.Sprintf("移除历史记录失败: %v", err), true)
		return
	}
	a.mu.Lock()
	a.historyRooms = a.cfgMgr.GetHistory()
	a.mu.Unlock()
	a.SetStatus(fmt.Sprintf("已删除历史: %s", code), false)
}

func (a *App) SaveSettings() {
	fontAlpha := max(0.0, min(1.0, 1.0-a.settingFontOpacity))
	recordPath := strings.TrimSpace(a.settingRecordPath)
	// Expand a leading ~ so users can type the familiar short form.
	if strings.HasPrefix(recordPath, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			recordPath = filepath.Join(home, strings.TrimPrefix(recordPath[1:], "/"))
		}
	}
	c := config.DmliveConfig{
		BCookie:            strings.TrimSpace(a.settingBCookie),
		CookiesFromBrowser: strings.TrimSpace(a.settingCookiesFromBrowser),
		DanmakuSpeed:       uint64(max(4, int(a.settingSpeed)) * 1000), // ms, Rust dmlive compatible
		FontAlpha:          fontAlpha,
		FontScale:          max(0.5, a.settingFontScale),
		RecordPath:         recordPath,
	}
	if err := a.dmliveCfg.Update(c); err != nil {
		a.SetStatus(fmt.Sprintf("保存设置失败: %v", err), true)
		return
	}
	a.showSettings = false
	a.SetStatus("设置已保存", false)
}

func (a *App) filterRooms(rooms []string) []string {
	var filtered []string
	searchQuery := strings.ToLower(strings.TrimSpace(a.searchFilter))

	for _, code := range rooms {
		// 1. Platform filter
		if a.platformFilter > 0 {
			switch a.platformFilter {
			case 1: // Bilibili
				if !strings.HasPrefix(code, "bi-") && !strings.HasPrefix(code, "BV") && !strings.HasPrefix(code, "av") && !strings.HasPrefix(code, "ep") && !strings.HasPrefix(code, "ss") {
					continue
				}
			case 2: // Douyu
				if !strings.HasPrefix(code, "do-") {
					continue
				}
			case 3: // Huya
				if !strings.HasPrefix(code, "hu-") {
					continue
				}
			case 4: // YouTube
				if !strings.HasPrefix(code, "yt-") && !strings.HasPrefix(code, "ytv-") {
					continue
				}
			case 5: // Twitch
				if !strings.HasPrefix(code, "tw-") {
					continue
				}
			case 6: // Douyin
				if !strings.HasPrefix(code, "dy-") {
					continue
				}
			}
		}

		info := a.roomInfos[code]
		isVideo := strings.HasPrefix(code, "BV") ||
			strings.HasPrefix(code, "av") ||
			strings.HasPrefix(code, "ep") ||
			strings.HasPrefix(code, "ss") ||
			strings.HasPrefix(code, "ytv-")

		// 2. Only living filter
		if a.onlyLiving {
			if isVideo || info == nil || !info.IsLiving {
				continue
			}
		}

		// 3. Search query filter
		if searchQuery != "" {
			matched := strings.Contains(strings.ToLower(code), searchQuery)
			if info != nil {
				if strings.Contains(strings.ToLower(info.Title), searchQuery) ||
					strings.Contains(strings.ToLower(info.Owner), searchQuery) {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}

		filtered = append(filtered, code)
	}

	return filtered
}

func (a *App) View(c *ui.Context) {
	t := c.Theme()

	// Keyboard shortcuts
	if c.Shortcut(ui.Alt, ui.KeyR) {
		a.Refresh()
	}
	if c.Shortcut(ui.Alt, ui.KeyQ) {
		mygo.App.Quit()
	}

	a.mu.RLock()
	saved := a.savedRooms
	history := a.historyRooms
	refreshing := a.refreshing
	a.mu.RUnlock()

	ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
		// 1. Top bar: Brand + Input + Actions + Settings
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Revda").FontSize(22).Bold().TextColor(t.Accent)

			// Search / Input box
			inputEl := ui.TextInput(c.Key("input_box"), &a.inputRoom).
				Placeholder("输入直播间代码或网址 (如 bi-545342, do-475252, hu-660118, dy-271387799639, BV...)...").
				Grow(1)

			if inputEl.Submitted() {
				a.Open(a.inputRoom)
				a.inputRoom = ""
			}

			if ui.PrimaryButton(c.Key("btn_play_top"), "播放").Clicked() {
				a.Open(a.inputRoom)
				a.inputRoom = ""
			}

			if ui.Button(c.Key("btn_save_top"), "收藏").Clicked() {
				a.Save(a.inputRoom)
				a.inputRoom = ""
			}

			refreshLabel := "刷新"
			if refreshing {
				refreshLabel = "刷新中..."
			}
			if ui.Button(c.Key("btn_refresh"), refreshLabel).Disabled(refreshing).Clicked() {
				a.Refresh()
			}

			if ui.Button(c.Key("btn_settings"), "设置").Clicked() {
				a.showSettings = true
			}
		})

		// 3. Tabs row & Filter toolbar
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(16).Children(func() {
			savedLabel := fmt.Sprintf("收藏 (%d)", len(saved))
			historyLabel := fmt.Sprintf("历史 (%d)", len(history))
			if ui.Tabs(c, &a.tabIndex, savedLabel, historyLabel).Changed() {
				if a.win != nil {
					a.win.Invalidate()
				}
			}

			ui.Spacer(c)

			// Platform chips
			platforms := []string{"全部", "B站", "斗鱼", "虎牙", "YouTube", "Twitch", "抖音"}
			ui.Row(c).Gap(4).Children(func() {
				for idx, name := range platforms {
					btn := ui.Button(c.Key(fmt.Sprintf("plat_%d", idx)), name)
					if a.platformFilter == idx {
						btn.Background(t.Accent).TextColor(t.AccentText)
					}
					if btn.Clicked() {
						a.platformFilter = idx
					}
				}
			})

			// Only living toggle chip
			liveBtn := ui.Button(c.Key("living_toggle"), "● 仅开播")
			if a.onlyLiving {
				liveBtn.Background(t.Success).TextColor(ui.RGB(255, 255, 255))
			}
			if liveBtn.Clicked() {
				a.onlyLiving = !a.onlyLiving
			}

			// Search filter inside current list
			ui.TextInput(c.Key("search_box"), &a.searchFilter).
				Placeholder("在列表中搜索...").
				Width(160)
		})

		tab := a.tabIndex
		var sourceRooms []string
		if tab == 0 {
			sourceRooms = saved
		} else {
			sourceRooms = make([]string, len(history))
			for i := range history {
				sourceRooms[i] = history[len(history)-1-i]
			}
		}

		currentRooms := a.filterRooms(sourceRooms)

		// 4. Room cards grid
		if len(currentRooms) == 0 {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				emptyText := "暂无匹配的房间"
				if len(sourceRooms) == 0 {
					if tab == 0 {
						emptyText = "暂无收藏的房间，可在上方输入直播间代码/链接后点击【收藏】或【播放】"
					} else {
						emptyText = "暂无播放历史记录"
					}
				}
				ui.Text(c, emptyText).FontSize(14).TextColor(t.TextMuted)
			})
		} else {
			ui.GridView(c, &a.gridState, len(currentRooms), 250, 245, func(i int) {
				a.renderCard(c, currentRooms[i])
			}).Grow(1).Gap(14)
		}
	})

	// 5. Settings Modal
	if a.showSettings {
		ui.Modal(c, &a.showSettings, func() {
			ui.Column(c).Width(460).Gap(14).Children(func() {
				// Title & Close
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, "播放与弹幕设置").FontSize(18).Bold()
					ui.Spacer(c)
					if ui.Button(c.Key("close_settings_btn"), "✕").Clicked() {
						a.showSettings = false
					}
				})

				ui.Divider(c)

				// 外观主题
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Text(c, "界面外观主题:").Width(140)
					themes := []struct {
						id   string
						name string
					}{
						{"system", "跟随系统"},
						{"dark", "深色模式"},
						{"light", "浅色模式"},
					}
					ui.Row(c).Gap(6).Children(func() {
						for _, th := range themes {
							btn := ui.Button(c.Key("th_"+th.id), th.name)
							if a.currentTheme == th.id {
								btn.Background(t.Accent).TextColor(t.AccentText)
							}
							if btn.Clicked() {
								a.SetTheme(th.id)
							}
						}
					})
				})

				ui.Divider(c)

				// 弹幕大小
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Text(c, fmt.Sprintf("弹幕大小倍率: %.1fx", a.settingFontScale)).Width(160)
					ui.Slider(c.Key("slider_scale"), &a.settingFontScale, 0.5, 2.5).Grow(1)
				})

				// 弹幕不透明度
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Text(c, fmt.Sprintf("弹幕不透明度: %d%%", int(a.settingFontOpacity*100))).Width(160)
					ui.Slider(c.Key("slider_alpha"), &a.settingFontOpacity, 0.1, 1.0).Grow(1)
				})

				// 弹幕持续时间
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Text(c, fmt.Sprintf("弹幕屏幕时长: %d 秒", int(a.settingSpeed))).Width(160)
					ui.Slider(c.Key("slider_speed"), &a.settingSpeed, 4.0, 20.0).Grow(1)
				})

				ui.Divider(c)

				// B站 Cookie
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, "Bilibili Cookie (用于解锁 1080P60 / 4K 最高画质):").FontSize(12).TextColor(t.TextMuted)
					ui.TextInput(c.Key("input_bcookie"), &a.settingBCookie).
						Placeholder("填入 SESSDATA=xxxx 或完整 Cookie...").
						FillWidth()
				})

				// 浏览器 Cookie 导入
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, "从浏览器自动获取 Cookie (可选: chrome / chromium / firefox):").FontSize(12).TextColor(t.TextMuted)
					ui.TextInput(c.Key("input_browser_cookie"), &a.settingCookiesFromBrowser).
						Placeholder("chrome / firefox / chromium (留空则不读取)").
						FillWidth()
				})

				ui.Divider(c)

				// 录制保存路径
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, "录制文件保存路径 (留空则使用默认的 ~/Videos/revda):").FontSize(12).TextColor(t.TextMuted)
					ui.TextInput(c.Key("input_record_path"), &a.settingRecordPath).
						Placeholder("~/Videos/revda").
						FillWidth()
				})

				// 默认录制
				ui.Checkbox(c.Key("cb_record"), &a.settingRecordByDefault, "默认以录制模式启动播放 (录制直播流并内嵌弹幕字幕轨)")

				ui.Spacer(c)

				// Save button
				ui.Row(c).FillWidth().Justify(ui.End).Gap(10).Children(func() {
					if ui.Button(c.Key("cancel_settings"), "取消").Clicked() {
						a.showSettings = false
					}
					if ui.PrimaryButton(c.Key("save_settings_btn"), "保存设置").Clicked() {
						a.SaveSettings()
					}
				})
			})
		})
	}

	// 6. Status toast: floats over the window's top-right corner, taking no
	// room in the layout below it.
	now := time.Now()
	showing, nextFrame := a.expireStatus(now)
	statusMsg, statusIsErr := a.statusSnapshot()

	if showing {
		bgColor := t.Surface
		textColor := t.Text
		borderColor := t.Border
		if statusIsErr {
			bgColor = t.Danger
			textColor = ui.RGB(255, 255, 255)
			borderColor = t.Danger
		}
		ui.Overlay(c, func() {
			toast := ui.Box(c).Key("status_toast").
				Absolute().
				Top(12).
				Right(12).
				MaxWidth(420).
				Radius(8).
				Background(bgColor).
				Border(1, borderColor).
				Shadow(0, 4, 16, 0, ui.RGBA(0, 0, 0, 0.18)).
				Transition(ui.ElementTransition{
					Duration: 180 * time.Millisecond,
					Enter:    &ui.Motion{X: 12, Opacity: 0},
					Exit:     &ui.Motion{X: 12, Opacity: 0},
				})
			toast.Children(func() {
				ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, statusMsg).FontSize(13).TextColor(textColor)
					if ui.Button(c.Key("status_toast_close"), "✕").Clicked() {
						a.DismissStatus()
					}
				})
			})
		})

		// Nothing in the layout depends on the toast, so ask for a frame
		// around the time it hides itself, and again when its exit animation
		// is due to end.
		if a.win != nil && !nextFrame.IsZero() {
			go func(at time.Time) {
				time.Sleep(time.Until(at))
				if a.win != nil {
					a.win.Invalidate()
				}
			}(nextFrame)
		}
	}
}

func (a *App) renderCard(c *ui.Context, code string) {
	t := c.Theme()
	a.mu.RLock()
	info := a.roomInfos[code]
	isLoading := a.loading[code]
	tab := a.tabIndex
	isSavedRoom := false
	for _, s := range a.savedRooms {
		if s == code {
			isSavedRoom = true
			break
		}
	}
	a.mu.RUnlock()

	title := code
	owner := "加载中..."
	cover := ""
	isLiving := false

	if info != nil {
		if info.Title != "" {
			title = info.Title
		}
		if info.Owner != "" {
			owner = info.Owner
		}
		cover = info.Cover
		isLiving = info.IsLiving
	}

	card := ui.Column(c.Key("card_"+code)).
		Fill().
		Radius(8).
		Background(t.Surface).
		Border(1, t.Border).
		Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.05))

	if card.DoubleClicked() {
		a.Open(code)
	}

	// Right-click Context Menu
	card.ContextMenu(func(m *ui.Menu) {
		if player.CanResolveDirectly(code) {
			if m.Item("开始播放").Chosen() {
				a.OpenDirect(code, false)
			}
		}
		if m.Item("播放并录制").Chosen() {
			a.OpenWithRecord(code)
		}
		m.Separator()
		if m.Item("复制链接").Chosen() {
			_, playURL, err := sites.NormalizeInput(code)
			if err == nil {
				c.WriteClipboard(playURL)
				a.SetStatus("已复制直播间链接到剪贴板", false)
			}
		}
		if m.Item("在浏览器打开").Chosen() {
			_, playURL, err := sites.NormalizeInput(code)
			if err == nil {
				c.OpenURL(playURL)
			}
		}
		m.Separator()
		if isSavedRoom {
			if m.Item("取消收藏").Chosen() {
				a.RemoveSaved(code)
			}
		} else {
			if m.Item("添加收藏").Chosen() {
				a.Save(code)
			}
		}
		if tab == 1 {
			if m.Item("从历史记录中删除").Chosen() {
				a.RemoveHistory(code)
			}
		}
	})

	card.Children(func() {
		// Cover image area
		coverBox := ui.Box(c.Key("cbox_"+code)).
			FillWidth().
			Height(130).
			Radius(8, 8, 0, 0).
			Background(t.SurfacePressed).
			Cursor(ui.CursorPointer)

		if coverBox.Clicked() {
			a.Open(code)
		}

		coverBox.Children(func() {
			// Platform mark, floating over the cover's top-right: the cover is
			// the card's loudest area, and an absolute icon there costs the
			// layout below it nothing. A white plate carries each logo, which
			// is colored, so it reads over any cover. PassThrough keeps the
			// cover clickable under it.
			if src := platformIcon(code); src != nil {
				ui.Box(c.Key("picon_"+code)).
					Absolute().
					Top(6).
					Right(6).
					Size(24, 24).
					Radius(6).
					Background(ui.RGBA(255, 255, 255, 0.92)).
					Center().
					PassThrough().
					Children(func() {
						switch {
						case src.svg != nil:
							ui.Image(c, src.svg).Size(16, 16)
						case src.bitmap != nil:
							ui.Image(c, src.bitmap).Size(16, 16).Fit(ui.Contain)
						}
					})
			}

			if cover != "" {
				if bmp := a.imgCache.Get(cover); bmp != nil {
					ui.Image(c.Key("img_"+code), bmp).
						FillWidth().
						Height(130).
						Fit(ui.Cover).
						Radius(8, 8, 0, 0)
				} else {
					ui.Column(c).Fill().Center().Children(func() {
						ui.Text(c, "封面加载中...").FontSize(11).TextColor(t.TextMuted)
					})
				}
			} else {
				ui.Column(c).Fill().Center().Children(func() {
					txt := "无封面"
					if isLoading {
						txt = "正在获取信息..."
					}
					ui.Text(c, txt).FontSize(11).TextColor(t.TextMuted)
				})
			}
		})

		// Room status row: living tag + code
		isVid := strings.HasPrefix(code, "BV") ||
			strings.HasPrefix(code, "av") ||
			strings.HasPrefix(code, "ep") ||
			strings.HasPrefix(code, "ss") ||
			strings.HasPrefix(code, "ytv-")

		ui.Row(c).FillWidth().Padding(4, 8).AlignItems(ui.Center).Children(func() {
			if isVid {
				tag := "视频"
				if strings.HasPrefix(code, "ep") || strings.HasPrefix(code, "ss") {
					tag = "番剧"
				}
				ui.Badge(c, tag).Background(t.SurfacePressed).TextColor(t.Accent)
			} else if isLiving {
				ui.Badge(c, "直播中").Background(t.Success).TextColor(ui.RGB(255, 255, 255))
			} else {
				ui.Badge(c, "未开播").Background(t.SurfacePressed).TextColor(t.TextMuted)
			}
			ui.Spacer(c)
			ui.Text(c, code).FontSize(11).TextColor(t.TextMuted)
		})

		// Details: title + owner
		ui.Column(c).FillWidth().Padding(0, 8, 4, 8).Gap(2).Children(func() {
			ui.Text(c, title).Bold().FontSize(13).SingleLine()
			ui.Text(c, owner).FontSize(12).TextColor(t.TextMuted).SingleLine()
		})

		ui.Spacer(c)

		// Bottom actions
		ui.Row(c).FillWidth().Padding(4, 8, 8, 8).Gap(6).Justify(ui.End).Children(func() {
			if ui.PrimaryButton(c.Key("p_"+code), "播放").Clicked() {
				a.Open(code)
			}

			if tab == 0 {
				if ui.Button(c.Key("unstar_"+code), "取消收藏").Clicked() {
					a.RemoveSaved(code)
				}
			} else {
				if isSavedRoom {
					ui.Text(c, "已收藏").FontSize(12).TextColor(t.Accent).AlignSelf(ui.Center)
				} else {
					if ui.Button(c.Key("star_"+code), "收藏").Clicked() {
						a.Save(code)
					}
				}
				if ui.Button(c.Key("del_"+code), "删除").Clicked() {
					a.RemoveHistory(code)
				}
			}
		})
	})
}
