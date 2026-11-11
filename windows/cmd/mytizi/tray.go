package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/kadaliao/mytizi/windows/internal/appcore"
	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

// trayController 是任务栏通知区域的图标与右键菜单（对应 macOS 版的菜单栏图标）。
type trayController struct {
	ui     *ui
	tray   *application.SystemTray
	menu   *application.Menu
	status *application.MenuItem
	toggle *application.MenuItem

	mu   sync.Mutex
	last string // 上一次渲染的摘要，没变化就不碰原生菜单
}

func newTrayController(u *ui) *trayController {
	t := &trayController{ui: u}
	t.tray = u.app.SystemTray.New()
	t.tray.SetIcon(icon("tray-off.ico"))
	t.tray.SetTooltip(appinfo.DisplayName)

	t.menu = u.app.NewMenu()
	t.status = t.menu.Add("未连接").SetEnabled(false)
	t.menu.AddSeparator()
	t.toggle = t.menu.Add("连接").OnClick(func(*application.Context) { go u.core.Toggle() })
	t.menu.Add("打开主窗口").OnClick(func(*application.Context) { u.show() })
	t.menu.AddSeparator()
	t.menu.Add("复制诊断信息（给 AI 排查）").OnClick(func(*application.Context) { go u.core.CopyDiagnostics(false) })
	t.menu.AddSeparator()
	t.menu.Add("退出 " + appinfo.DisplayName).OnClick(func(*application.Context) { u.quit() })
	t.tray.SetMenu(t.menu)

	// 左键单击打开主窗口，右键弹菜单（Windows 的习惯）。
	t.tray.OnClick(func() { u.show() })
	t.tray.OnDoubleClick(func() { u.show() })
	return t
}

func (t *trayController) render(s appcore.UIState) {
	statusText, toggleText, toggleEnabled, connected := describe(s)
	key := statusText + "|" + toggleText + "|" + map[bool]string{true: "1", false: "0"}[toggleEnabled]
	t.mu.Lock()
	changed := key != t.last
	t.last = key
	t.mu.Unlock()
	if !changed {
		return
	}
	application.InvokeAsync(func() {
		t.status.SetLabel(statusText)
		t.toggle.SetLabel(toggleText)
		t.toggle.SetEnabled(toggleEnabled)
		t.menu.Update()
		if connected {
			t.tray.SetIcon(icon("tray-on.ico"))
		} else {
			t.tray.SetIcon(icon("tray-off.ico"))
		}
		t.tray.SetTooltip(appinfo.DisplayName + " · " + statusText)
	})
}

func describe(s appcore.UIState) (status, toggle string, toggleEnabled, connected bool) {
	switch s.Phase {
	case appcore.PhaseConnected:
		status = "已连接"
		if s.ActiveNode != "" {
			status += " · " + s.ActiveNode
		}
		return status, "断开", true, true
	case appcore.PhaseConnecting:
		return "正在连接…", "连接", false, false
	case appcore.PhaseDisconnecting:
		return "正在断开…", "断开", false, false
	case appcore.PhaseFailed:
		return "连接失败", "重新连接", true, false
	case appcore.PhaseNeedsSubscription:
		return "尚未添加订阅", "连接", false, false
	default:
		return "未连接", "连接", true, false
	}
}
