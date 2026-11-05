package app

import (
	"github.com/egoist/mygo/ui"
)

// renderHelp draws the in-app usage guide: input formats per platform and
// the gestures/settings users otherwise only find in the README.
func (a *App) renderHelp(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.showHelp, func() {
		ui.Column(c).Width(640).Height(620).Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "使用帮助").FontSize(18).Bold()
				ui.Spacer(c)
				if ui.Button(c.Key("close_help_btn"), "✕").Clicked() {
					a.showHelp = false
				}
			})

			ui.Divider(c)

			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					section := func(title string, lines ...string) {
						ui.Column(c).FillWidth().Gap(2).Children(func() {
							ui.Text(c, title).Bold().FontSize(14)
							for _, line := range lines {
								ui.Text(c, line).FontSize(12).TextColor(t.TextMuted)
							}
						})
					}

					section("输入格式",
						"B站直播：bi-6（或 live.bilibili.com/6 链接）",
						"斗鱼：do-9999　虎牙：hu-660118",
						"抖音：dy-548507406983（live.douyin.com 的 web_rid）",
						"YouTube 直播：yt-LofiGirl（不带 @）　视频：ytv-视频ID",
						"Twitch：tw-频道名",
						"B站视频：BV1xx / av123，多 P 用 BV1xx:2",
						"番剧：ep123 / ss456，选集用 ss456:3",
						"也可以直接粘贴上面的平台链接；纯数字房间号需要带平台前缀。",
					)

					section("播放与操作",
						"回车或「播放」开始播放；「收藏」把当前地址加入收藏页。",
						"双击卡片播放；右键卡片可播放、播放并录制、复制链接、在浏览器打开。",
						"Alt+R 刷新封面与开播状态；Alt+Q 退出；F1 打开本帮助。",
					)

					section("录制与弹幕",
						"右键「播放并录制」，或在设置中开启「默认以录制模式启动」。",
						"文件保存到设置中的路径（默认 ~/Videos/revda），格式为 MKV。",
						"弹幕会作为 ASS 字幕轨内嵌，播放时可用字幕键开关。",
						"弹幕大小、透明度、屏幕停留时间、覆盖高度与位置都在设置中调整。",
						"依赖 mpv 与 ffmpeg，需要已安装并在 PATH 中。",
					)

					section("Cookie 与画质",
						"上方 Cookie 仅用于 B站（直播取流与视频清晰度），不会发送给其他平台。",
						"填入 SESSDATA 等登录 Cookie 可解锁 1080P60 / 4K。",
						"「从浏览器自动获取」支持 chrome / chromium / firefox，只读取 .bilibili.com 的 Cookie；填写后优先于上面的 Cookie。",
						"Windows 上 Chrome 127+ 的应用绑定加密（v20）暂不支持，请改用 Firefox 或手动填写 Cookie。",
					)

					section("说明",
						"YouTube 直播可正常播放（含音频），直播弹幕暂不可用。",
						"各平台可能对频繁刷新限流；播放失败时先确认 mpv/ffmpeg 与网络。",
					)
				})
			})
		})
	})
}
