package main

import (
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/kadaliao/mytizi/windows/internal/appcore"
	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
)

// App 是绑定给前端的服务：前端用 wails.Call.ByName('main.App.<方法>') 调用。
// 耗时的方法（连接、诊断）会一直等到完成；界面靠 "state" 事件刷新，不靠返回值。
type App struct {
	core *appcore.Core
	ui   *ui
}

func (a *App) State() appcore.UIState         { return a.core.State() }
func (a *App) AddSubscription(link string)    { a.core.AddSubscription(link) }
func (a *App) Toggle()                        { a.core.Toggle() }
func (a *App) Reconnect()                     { a.core.Reconnect() }
func (a *App) RefreshSubscription()           { a.core.RefreshSubscription() }
func (a *App) SelectNode(name string)         { a.core.SelectNode(name) }
func (a *App) TestLatency()                   { a.core.TestLatency() }
func (a *App) CopyDiagnostics(full bool)      { a.core.CopyDiagnostics(full) }
func (a *App) OpenLogsFolder()                { a.core.OpenLogsFolder() }
func (a *App) OpenCustomRules()               { a.core.OpenCustomRules() }
func (a *App) SetSetting(key string, on bool) { a.core.SetSetting(key, on) }
func (a *App) RemoveSubscription()            { a.core.RemoveSubscription() }
func (a *App) CheckUpdates()                  { a.core.CheckUpdates() }
func (a *App) OpenUpdate()                    { a.core.OpenUpdate() }
func (a *App) SkipUpdate()                    { a.core.SkipUpdate() }
func (a *App) OpenProjectPage()               { a.core.OpenProjectPage() }
func (a *App) RepairService()                 { a.core.RepairService() }
func (a *App) Quit()                          { a.ui.quit() }

// ClipboardText 读剪贴板（"粘贴"按钮）。在 Go 这边读，网页里读剪贴板会弹权限提示。
func (a *App) ClipboardText() string {
	text, _ := a.ui.app.Clipboard.Text()
	return text
}

type ui struct {
	app    *application.App
	window *application.WebviewWindow
	tray   *trayController
	core   *appcore.Core

	// confirmedQuit 为 true 时不再询问（已经选了"断开并退出"）。
	confirmedQuit atomic.Bool
}

func runGUI(hidden bool) {
	user := paths.DefaultUser()
	_ = user.Prepare()
	u := &ui{}
	deps := newDeps(user)
	deps.SetClipboard = func(text string) error {
		if !u.app.Clipboard.SetText(text) {
			return errCopyFailed
		}
		return nil
	}
	deps.Emit = func(state appcore.UIState) {
		if u.app == nil {
			return
		}
		u.app.Event.Emit("state", state)
		if u.tray != nil {
			u.tray.render(state)
		}
	}
	u.core = appcore.New(deps)
	service := &App{core: u.core, ui: u}

	u.app = application.New(application.Options{
		Name:        appinfo.DisplayName,
		Description: "粘贴订阅链接，一键连接",
		Icon:        icon("app.png"),
		Services:    []application.Service{application.NewService(service)},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(frontendFS())},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.mytizi.app",
			// 再次打开 App（开始菜单、桌面图标）时把已有窗口带到前面。
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { u.show() },
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
			WebviewUserDataPath:           webviewDataDir(user),
		},
		Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
		// 托盘"退出"与系统注销都走这里：连接中先问一句。
		ShouldQuit: u.shouldQuit,
	})

	u.window = u.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                "main",
		Title:               appinfo.DisplayName,
		Width:               windowWidth,
		Height:              windowHeight,
		DisableResize:       true,
		MaximiseButtonState: application.ButtonHidden,
		URL:                 "/",
		Hidden:              hidden,
		InitialPosition:     application.WindowCentered,
		BackgroundColour:    windowBackground(),
	})
	// 关闭窗口只是隐藏：App 留在托盘里，继续自动刷新订阅、监测线路。
	u.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		u.window.Hide()
		e.Cancel()
	})
	u.window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) { fixClientSize(u.window) })

	u.tray = newTrayController(u)
	u.app.OnShutdown(func() { u.core.Stop() })

	go func() {
		// 等事件系统就绪再开始轮询，第一份状态能送到界面。
		time.Sleep(300 * time.Millisecond)
		u.core.Start()
	}()
	if err := u.app.Run(); err != nil {
		panic(err)
	}
}

func (u *ui) show() {
	application.InvokeAsync(func() {
		u.window.Show()
		u.window.Restore()
		u.window.Focus()
	})
}

func (u *ui) quit() {
	application.InvokeAsync(func() { u.app.Quit() })
}

// shouldQuit：连接中退出时问一下是断开还是保持（后台服务会继续维持连接）。
func (u *ui) shouldQuit() bool {
	if u.confirmedQuit.Load() || !u.core.IsConnected() {
		return true
	}
	decision := make(chan string, 1)
	dialog := u.app.Dialog.Question().
		SetTitle("还在连接中").
		SetMessage("退出 MyTizi 时要断开连接吗？\n\n选择“保持连接”，网络会继续走代理，之后重新打开 MyTizi 即可管理。")
	dialog.AddButton("断开并退出").OnClick(func() { decision <- "disconnect" })
	dialog.AddButton("保持连接并退出").OnClick(func() { decision <- "keep" })
	dialog.AddButton("取消").SetAsCancel().OnClick(func() { decision <- "cancel" })
	dialog.Show()
	var choice string
	select {
	case choice = <-decision:
	default:
		choice = "cancel"
	}
	switch choice {
	case "disconnect":
		go func() {
			u.core.Disconnect()
			u.confirmedQuit.Store(true)
			u.quit()
		}()
		return false
	case "keep":
		return true
	default:
		return false
	}
}

// windowBackground 与前端的背景色一致，避免深色模式下打开窗口时先闪一下白色。
func windowBackground() application.RGBA {
	if prefersDark() {
		return application.NewRGB(32, 32, 32)
	}
	return application.NewRGB(243, 243, 243)
}

type quitError string

func (e quitError) Error() string { return string(e) }

const errCopyFailed = quitError("无法写入剪贴板")
