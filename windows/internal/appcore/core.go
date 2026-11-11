// Package appcore 是 App 的状态机（macOS 版 AppModel 的移植）：订阅、连接、轮询、诊断、更新提示。
// 不依赖界面框架：状态变化通过 Deps.Emit 推出去，界面与托盘各自渲染。
package appcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/mihomo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/redact"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/subscription"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
	"github.com/kadaliao/mytizi/windows/internal/yamlx"
)

// ServiceClient 是后台服务的客户端（*ipc.Client 满足它；测试用假的）。
type ServiceClient interface {
	Hello(context.Context) (*ipc.Hello, error)
	Status(context.Context) (*ipc.Status, error)
	Start(context.Context, ipc.StartParams) (*ipc.Status, error)
	Stop(context.Context) (*ipc.Status, error)
	UpdateSubscription(context.Context, []byte) error
}

// UpdateInfo 是"有新版本"的结论。
type UpdateInfo struct {
	Version     string
	ReleaseURL  string
	DownloadURL string
}

// DiagContext 是 App 自己知道、要写进诊断报告的部分。
type DiagContext struct {
	AppPhase         string
	LastError        string
	Settings         settings.Settings
	Nodes            []NodeRow
	CustomRuleCount  int
	CustomRulesError string
}

// Deps 是状态机用到的外部能力。main 里接真实实现，测试里接假的。
type Deps struct {
	Client     ServiceClient
	Store      settings.Store
	Paths      paths.User
	InstallDir string
	Version    string

	// Probe 用内置核心解析订阅。
	Probe func(ctx context.Context, body []byte) ([]settings.Node, error)
	// Fetch 下载订阅。
	Fetch func(ctx context.Context, link string) (*subscription.Fetched, error)
	// Diagnose 生成诊断报告。
	Diagnose func(ctx context.Context, full bool, info DiagContext) string
	// CheckUpdate 查询最新版本；IsNewer 比较版本号。
	CheckUpdate func(ctx context.Context) (*UpdateInfo, error)
	IsNewer     func(candidate, current string) bool

	ServiceState  func() svcctl.State
	StartService  func() error
	RepairService func() error

	// LocalDNS 返回连接前系统使用的 IPv4 DNS（路由器）；Tailscale 返回是否开着 Tailscale。
	LocalDNS  func() []string
	Tailscale func() bool

	SetClipboard     func(string) error
	OpenURL          func(string) error
	OpenFile         func(string) error
	RevealFile       func(string) error
	LaunchAtLogin    func() bool
	SetLaunchAtLogin func(bool) error

	// Emit 在每次状态变化后被调用（不持有锁）。
	Emit func(UIState)
}

// Core 是状态机。所有导出方法都可以从任意 goroutine 调用。
type Core struct {
	d   Deps
	log *rotlog.Log

	mu                     sync.Mutex
	settings               settings.Settings
	phase                  string
	step                   string
	status                 *ipc.Status
	nodes                  []NodeRow
	selection              string
	activeNode             string
	errorMessage           string
	notice                 string
	noticeSeq              int
	connectivityIssue      string
	serviceProblem         string
	adding                 bool
	addProgress            string
	addError               string
	refreshing             bool
	testing                bool
	copying                bool
	checkingUpdate         bool
	repairing              bool
	availableUpdate        *UpdateInfo
	api                    *mihomo.Client
	statusFailures         int
	lastDelayRefresh       time.Time
	lastSubscriptionCheck  time.Time
	lastUpdateAttempt      time.Time
	lastConnectivityCheck  time.Time
	checkingConnectivity   bool
	triedStartingService   bool
	helloChecked           bool
	launchAtLogin          bool
	pollCancel             context.CancelFunc
	connectivityProblemSet bool
}

const (
	PhaseNeedsSubscription = "needsSubscription"
	PhaseDisconnected      = "disconnected"
	PhaseConnecting        = "connecting"
	PhaseConnected         = "connected"
	PhaseDisconnecting     = "disconnecting"
	PhaseFailed            = "failed"
)

// New 读取设置并建立初始状态。
func New(d Deps) *Core {
	_ = d.Paths.Prepare()
	c := &Core{d: d, log: rotlog.New(d.Paths.AppLog(), 1<<20, 2), selection: AutoSelection}
	c.settings = d.Store.Load()
	c.phase = PhaseDisconnected
	if c.settings.Subscription == nil {
		c.phase = PhaseNeedsSubscription
	} else {
		c.nodes = rowsFrom(c.settings.Subscription.Nodes)
	}
	if d.LaunchAtLogin != nil {
		c.launchAtLogin = d.LaunchAtLogin()
	}
	c.lastSubscriptionCheck = time.Now()
	c.logf("info", "App 启动 v%s", d.Version)
	return c
}

func rowsFrom(nodes []settings.Node) []NodeRow {
	rows := make([]NodeRow, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, NodeRow{Name: n.Name, Type: n.Type})
	}
	return rows
}

// logf 写 app.log。所有内容先脱敏：网络错误的描述里可能带着完整的订阅链接（含 token）。
func (c *Core) logf(level, format string, args ...any) {
	r := redact.New("")
	c.mu.Lock()
	if c.settings.Subscription != nil {
		r.AddSubscriptionURL(c.settings.Subscription.URL)
	}
	c.mu.Unlock()
	c.logRedacted(r, level, format, args...)
}

func (c *Core) logRedacted(r *redact.Redactor, level, format string, args ...any) {
	prefix := "INFO  "
	if level == "error" {
		prefix = "ERROR "
	}
	c.log.Line("%s", prefix+r.Redact(fmt.Sprintf(format, args...)))
}

// MARK: 状态

// change 在锁内修改状态，然后推送一次快照。
func (c *Core) change(f func()) {
	c.mu.Lock()
	f()
	state := c.snapshotLocked()
	c.mu.Unlock()
	if c.d.Emit != nil {
		c.d.Emit(state)
	}
}

// State 返回当前快照。
func (c *Core) State() UIState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *Core) snapshotLocked() UIState {
	s := UIState{
		Phase: c.phase, Step: c.step, Version: c.d.Version,
		Nodes: append([]NodeRow(nil), c.nodes...), Selection: c.selection, ActiveNode: c.activeNode,
		ErrorMessage: c.errorMessage, Notice: c.notice, ConnectivityIssue: c.connectivityIssue,
		ServiceProblem: c.serviceProblem, AddingSubscription: c.adding, AddProgress: c.addProgress,
		AddError: c.addError, RefreshingSubscription: c.refreshing, TestingLatency: c.testing,
		CopyingDiagnostics: c.copying, CheckingUpdate: c.checkingUpdate, RepairingService: c.repairing,
		Settings: SettingsView{
			StrictRoute: c.settings.StrictRoute, AutoConnectOnLaunch: c.settings.AutoConnectOnLaunch,
			AutoCheckUpdates: c.settings.AutoCheckUpdates, LaunchAtLogin: c.launchAtLogin,
		},
	}
	if s.Nodes == nil {
		s.Nodes = []NodeRow{}
	}
	if c.phase == PhaseConnected && c.status != nil {
		s.StartedAt = c.status.StartedAt
	}
	if sub := c.settings.Subscription; sub != nil {
		view := &SubscriptionView{
			Name: sub.Name, Host: sub.Host(), NodeCount: len(sub.Nodes),
			UpdatedText: UpdatedText(sub.UpdatedAt, time.Now()),
		}
		if u := sub.Usage; u != nil && u.Total > 0 {
			fraction := float64(u.Used()) / float64(u.Total)
			if fraction > 1 {
				fraction = 1
			}
			view.Usage = &UsageView{Fraction: fraction, UsedText: Bytes(u.Used()), TotalText: Bytes(u.Total)}
			if u.Expire != nil {
				view.Usage.ExpireText = DateText(*u.Expire)
			}
		}
		s.Subscription = view
	}
	if c.availableUpdate != nil {
		s.AvailableUpdate = &UpdateView{Version: c.availableUpdate.Version}
	}
	return s
}

func (c *Core) isBusyLocked() bool {
	return c.phase == PhaseConnecting || c.phase == PhaseDisconnecting
}

// ShowNotice 显示一条几秒后自动消失的提示。
func (c *Core) ShowNotice(text string, d time.Duration) {
	if d <= 0 {
		d = 3 * time.Second
	}
	var seq int
	c.change(func() {
		c.noticeSeq++
		seq = c.noticeSeq
		c.notice = text
	})
	go func() {
		time.Sleep(d)
		c.mu.Lock()
		current := c.noticeSeq == seq
		c.mu.Unlock()
		if current {
			c.change(func() { c.notice = "" })
		}
	}()
}

// PhaseDescription 是给诊断报告与托盘看的阶段描述。
func (c *Core) PhaseDescription() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.phase {
	case PhaseNeedsSubscription:
		return "尚未添加订阅"
	case PhaseDisconnected:
		return "未连接"
	case PhaseConnecting:
		return "连接中（" + c.step + "）"
	case PhaseConnected:
		return "已连接"
	case PhaseDisconnecting:
		return "正在断开"
	default:
		return "失败"
	}
}

// MARK: 生命周期

// Start 开始轮询后台状态；只调用一次。
func (c *Core) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.pollCancel != nil {
		c.mu.Unlock()
		cancel()
		return
	}
	c.pollCancel = cancel
	c.mu.Unlock()
	go func() {
		// 先接管一次现状：App 重新打开时，后台可能还保持着连接。
		c.poll(ctx)
		c.mu.Lock()
		auto := c.settings.AutoConnectOnLaunch && c.phase == PhaseDisconnected
		c.mu.Unlock()
		if auto {
			c.Connect()
		}
		go func() {
			// 启动后稍等一会再检查更新：不跟连接、订阅刷新抢时间。
			select {
			case <-time.After(8 * time.Second):
				c.checkForUpdatesIfDue()
			case <-ctx.Done():
			}
		}()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.poll(ctx)
			}
		}
	}()
}

// Stop 停止轮询（退出 App 时）。不会断开连接。
func (c *Core) Stop() {
	c.mu.Lock()
	cancel := c.pollCancel
	c.pollCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.log.Close()
}

// IsConnected 供退出确认使用。
func (c *Core) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase == PhaseConnected
}

// MARK: 轮询

func (c *Core) poll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	st, err := c.d.Client.Status(ctx)
	if err != nil {
		c.handleServiceUnavailable(err)
		return
	}
	if !c.ensureHelloChecked(ctx) {
		return
	}
	var api *mihomo.Client
	var becameConnected, running bool
	c.change(func() {
		c.statusFailures = 0
		c.serviceProblem = ""
		c.status = st
		if c.isBusyLocked() {
			return
		}
		switch st.Phase {
		case ipc.PhaseRunning:
			running = true
			if c.phase != PhaseConnected {
				c.phase = PhaseConnected
				c.errorMessage = ""
				becameConnected = true
			}
			if st.ControllerPort > 0 && st.ControllerSecret != "" &&
				(c.api == nil || c.api.Port != st.ControllerPort || c.api.Secret != st.ControllerSecret) {
				c.api = mihomo.New(st.ControllerPort, st.ControllerSecret)
			}
			api = c.api
		case ipc.PhaseCrashed:
			// 只在亲眼看着它从"已连接"掉下来时才报失败；启动时读到很久以前的崩溃记录不打扰用户。
			if c.phase == PhaseConnected {
				c.phase = PhaseFailed
				c.errorMessage = "连接意外中断，网络已自动恢复。可以重新连接，或点“遇到问题”复制诊断信息。"
				c.logRedacted(redact.New(""), "error", "核心意外退出：%+v", st.LastExit)
			}
			c.api = nil
		case ipc.PhaseStopped:
			if c.phase == PhaseConnected {
				c.phase = c.idlePhaseLocked()
			}
			c.api = nil
			c.activeNode = ""
		}
	})
	if becameConnected {
		c.logf("info", "检测到后台已在运行，接管当前连接")
	}
	if running && api != nil {
		c.refreshRuntimeInfo(ctx, api)
		c.refreshSubscriptionIfDue()
		c.checkForUpdatesIfDue()
	}
}

// ensureHelloChecked 第一次连上服务时核对协议版本。
func (c *Core) ensureHelloChecked(ctx context.Context) bool {
	c.mu.Lock()
	checked := c.helloChecked
	c.mu.Unlock()
	if checked {
		return true
	}
	hello, err := c.d.Client.Hello(ctx)
	if err != nil {
		return true // 下一轮再试；status 已经通了
	}
	if err := ipc.CheckHello(hello); err != nil {
		c.change(func() { c.serviceProblem = err.Error() })
		return false
	}
	c.mu.Lock()
	c.helloChecked = true
	c.mu.Unlock()
	return true
}

func (c *Core) handleServiceUnavailable(err error) {
	var ce *ipc.ClientError
	notRunning := errors.As(err, &ce) && ce.NotRunning
	state := svcctl.Unknown
	if notRunning && c.d.ServiceState != nil {
		state = c.d.ServiceState()
	}
	// 服务装了但停着（例如被手动停止）：普通用户就能拉起来，先自己试一次。
	c.mu.Lock()
	tryStart := state == svcctl.Stopped && !c.triedStartingService && c.d.StartService != nil
	if tryStart {
		c.triedStartingService = true
	}
	c.mu.Unlock()
	if tryStart {
		if startErr := c.d.StartService(); startErr == nil {
			c.logf("info", "后台服务没有运行，已自动启动")
			return
		} else {
			c.logf("error", "自动启动后台服务失败：%v", startErr)
		}
	}
	c.change(func() {
		c.statusFailures++
		c.status = nil
		c.helloChecked = false
		switch {
		case state == svcctl.NotInstalled:
			c.serviceProblem = "后台组件没有安装，连接需要它。点“修复后台组件”安装。"
		case notRunning:
			c.serviceProblem = "后台组件没有运行，连接需要它。点“修复后台组件”重新安装并启动。"
		case c.statusFailures >= 2:
			c.serviceProblem = "后台组件没有响应：" + err.Error()
		}
		// 状态查询偶尔会慢一拍；连续两次失败才把"已连接"翻成失败，避免界面闪跳。
		if c.phase == PhaseConnected && c.statusFailures >= 2 {
			c.phase = PhaseFailed
			c.errorMessage = "与后台组件的连接中断了。"
		}
	})
}

func (c *Core) idlePhaseLocked() string {
	if c.settings.Subscription == nil {
		return PhaseNeedsSubscription
	}
	return PhaseDisconnected
}

func (c *Core) refreshRuntimeInfo(ctx context.Context, api *mihomo.Client) {
	if group, err := api.Group(ctx, config.ProxyGroup); err == nil {
		chosen := group.Now
		if chosen == "" {
			chosen = AutoSelection
		}
		active := chosen
		if chosen == AutoSelection {
			if auto, err := api.Group(ctx, config.AutoGroup); err == nil {
				active = auto.Now
			}
		}
		c.change(func() {
			c.selection = chosen
			c.activeNode = active
		})
	}
	// 延迟记录变化慢，10 秒取一次。
	c.mu.Lock()
	due := time.Since(c.lastDelayRefresh) > 10*time.Second
	c.mu.Unlock()
	if due {
		if provider, err := api.Provider(ctx, config.ProviderName); err == nil {
			rows := make([]NodeRow, 0, len(provider.Proxies))
			for _, p := range provider.Proxies {
				rows = append(rows, NodeRow{Name: p.Name, Type: p.Type, Delay: p.LastDelay()})
			}
			c.change(func() {
				c.lastDelayRefresh = time.Now()
				c.nodes = rows
			})
		}
	}
	c.refreshConnectivityIfNeeded(api)
}

// refreshConnectivityIfNeeded：所有线路都没有延迟记录时，主动做一次整组测速。
// 健康检查是 lazy 的：核心不会因为"节点全挂"主动报错，所以 App 需要自己发现这件事。
func (c *Core) refreshConnectivityIfNeeded(api *mihomo.Client) {
	c.mu.Lock()
	if c.phase != PhaseConnected || c.checkingConnectivity {
		c.mu.Unlock()
		return
	}
	allUnmeasured := len(c.nodes) > 0
	for _, n := range c.nodes {
		if n.Delay > 0 {
			allUnmeasured = false
			break
		}
	}
	if !allUnmeasured {
		hadIssue := c.connectivityIssue != ""
		c.mu.Unlock()
		if hadIssue {
			c.reportConnectivity(false)
		}
		return
	}
	if time.Since(c.lastConnectivityCheck) < 5*time.Minute {
		c.mu.Unlock()
		return
	}
	c.lastConnectivityCheck = time.Now()
	c.checkingConnectivity = true
	url := c.settings.HealthCheckURL
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.checkingConnectivity = false
		c.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := api.GroupDelay(ctx, config.AutoGroup, url, 5000)
	if !c.IsConnected() {
		return
	}
	var httpErr *mihomo.HTTPError
	switch {
	case err == nil && len(result) == 0:
		c.reportConnectivity(true)
	case err == nil:
		c.change(func() {
			for i := range c.nodes {
				c.nodes[i].Delay = result[c.nodes[i].Name]
			}
		})
		c.reportConnectivity(false)
	case errors.As(err, &httpErr):
		// 504 = 整组都超时；控制接口本身连不上（transport）不是节点问题，不误报。
		c.reportConnectivity(true)
	}
}

func (c *Core) reportConnectivity(problem bool) {
	var changed bool
	c.change(func() {
		if problem && c.connectivityIssue == "" {
			c.connectivityIssue = "所有线路都连不上：节点服务器可能已经不可用，或者当前网络挡住了代理协议。"
			changed = true
		} else if !problem && c.connectivityIssue != "" {
			c.connectivityIssue = ""
			changed = true
		}
	})
	if changed && problem {
		c.logf("error", "所有线路测速都失败：代理出口不可用")
	} else if changed {
		c.logf("info", "线路已恢复可用")
	}
}

// MARK: 连接

// Toggle：已连接就断开，否则连接。
func (c *Core) Toggle() {
	c.mu.Lock()
	phase := c.phase
	c.mu.Unlock()
	switch phase {
	case PhaseConnected:
		c.Disconnect()
	case PhaseDisconnected, PhaseFailed:
		c.Connect()
	}
}

func (c *Core) Connect() {
	var sub *settings.Subscription
	proceed := false
	c.change(func() {
		if c.phase != PhaseDisconnected && c.phase != PhaseFailed {
			return
		}
		if c.settings.Subscription == nil {
			c.phase = PhaseNeedsSubscription
			return
		}
		sub = c.settings.Subscription
		c.errorMessage = ""
		c.phase = PhaseConnecting
		c.step = "正在检查后台组件…"
		proceed = true
	})
	if !proceed {
		return
	}
	body, err := c.d.Store.LoadSubscriptionBody()
	if err != nil || len(body) == 0 {
		c.fail(errors.New("找不到已保存的订阅内容，请更换订阅后重新添加。"))
		return
	}

	ctx := context.Background()
	if err := c.ensureService(ctx); err != nil {
		if errors.Is(err, svcctl.ErrCancelled) {
			c.change(func() { c.phase = c.idlePhaseLocked() })
			c.ShowNotice("已取消授权，没有安装后台组件", 4*time.Second)
			c.logf("info", "用户取消了管理员授权")
			return
		}
		c.fail(err)
		return
	}

	c.change(func() { c.step = "正在生成配置…" })
	params := ipc.StartParams{Subscription: body, StrictRoute: c.settingsSnapshot().StrictRoute,
		HealthCheckURL: c.settingsSnapshot().HealthCheckURL}
	if host := sub.Host(); host != "" {
		params.DirectHosts = []string{host}
	}
	if c.d.LocalDNS != nil {
		params.LocalDNS = c.d.LocalDNS()
	}
	if c.d.Tailscale != nil {
		params.TailscaleDNS = c.d.Tailscale()
	}
	if rules, err := c.d.Store.LoadCustomRules(); err != nil {
		c.logf("error", "自定义规则有误，已忽略：%v", err)
		c.ShowNotice("自定义规则有错误，已忽略（"+err.Error()+"）", 6*time.Second)
	} else {
		params.CustomRules = rules
	}
	c.writeRedactedConfig(params)

	c.change(func() { c.step = "正在启动…" })
	st, err := c.d.Client.Start(ctx, params)
	if err != nil {
		c.fail(err)
		if s, e := c.d.Client.Status(ctx); e == nil {
			c.change(func() { c.status = s })
		}
		return
	}
	c.change(func() {
		c.status = st
		c.api = mihomo.New(st.ControllerPort, st.ControllerSecret)
		c.phase = PhaseConnected
		c.step = ""
		c.serviceProblem = ""
		c.lastDelayRefresh = time.Time{}
		c.lastConnectivityCheck = time.Time{}
		c.connectivityIssue = ""
	})
	c.logf("info", "已连接（TUN %s）", st.TunInterface)
	c.mu.Lock()
	api := c.api
	c.mu.Unlock()
	c.refreshRuntimeInfo(ctx, api)
}

func (c *Core) settingsSnapshot() settings.Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

func (c *Core) fail(err error) {
	message := Message(err)
	c.change(func() {
		c.api = nil
		c.phase = PhaseFailed
		c.step = ""
		c.errorMessage = message
	})
	c.logf("error", "连接失败：%v", err)
}

// ensureService 确认后台服务可用：没运行就启动，没安装（或版本不对）就弹 UAC 修复。
func (c *Core) ensureService(ctx context.Context) error {
	hello, err := c.d.Client.Hello(ctx)
	if err == nil {
		if checkErr := ipc.CheckHello(hello); checkErr == nil {
			return nil
		}
	} else {
		var ce *ipc.ClientError
		if !errors.As(err, &ce) || !ce.NotRunning {
			return err
		}
		if c.d.ServiceState != nil && c.d.ServiceState() == svcctl.Stopped && c.d.StartService != nil {
			c.change(func() { c.step = "正在启动后台组件…" })
			if c.d.StartService() == nil {
				if hello, err = c.d.Client.Hello(ctx); err == nil && ipc.CheckHello(hello) == nil {
					return nil
				}
			}
		}
	}
	if c.d.RepairService == nil {
		return errors.New("后台组件没有运行，请重新运行安装程序。")
	}
	c.change(func() { c.step = "需要安装后台组件，请在弹出的窗口里允许…" })
	if err := c.d.RepairService(); err != nil {
		return err
	}
	c.change(func() { c.step = "正在等待后台组件…" })
	deadline := time.Now().Add(15 * time.Second)
	for {
		hello, err = c.d.Client.Hello(ctx)
		if err == nil {
			if checkErr := ipc.CheckHello(hello); checkErr != nil {
				return checkErr
			}
			c.mu.Lock()
			c.helloChecked = true
			c.mu.Unlock()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (c *Core) Disconnect() {
	proceed := false
	c.change(func() {
		if c.phase == PhaseConnected || c.phase == PhaseFailed || c.isBusyLocked() {
			c.phase = PhaseDisconnecting
			proceed = true
		}
	})
	if !proceed {
		return
	}
	st, err := c.d.Client.Stop(context.Background())
	c.change(func() {
		if err == nil {
			c.status = st
		}
		c.api = nil
		c.activeNode = ""
		c.connectivityIssue = ""
		c.phase = c.idlePhaseLocked()
	})
	if err != nil {
		c.logf("error", "断开失败：%v", err)
		c.ShowNotice("断开失败："+Message(err), 6*time.Second)
	} else {
		c.logf("info", "已断开")
	}
}

func (c *Core) Reconnect() {
	c.mu.Lock()
	phase := c.phase
	c.mu.Unlock()
	if phase == PhaseConnected || phase == PhaseFailed {
		c.Disconnect()
	}
	c.Connect()
}

// writeRedactedConfig 在用户目录留一份配置副本供排查：真实配置（含控制密钥）只在服务的目录里。
func (c *Core) writeRedactedConfig(p ipc.StartParams) {
	text := yamlx.Render(config.Build(config.Options{
		ControllerPort: 0, ControllerSecret: "<由后台服务生成>", TUN: true, StrictRoute: p.StrictRoute,
		DirectHosts: p.DirectHosts, CustomRules: p.CustomRules, HealthCheckURL: p.HealthCheckURL,
		LocalDNS: p.LocalDNS, TailscaleDNS: p.TailscaleDNS,
	}))
	header := "# 最近一次连接使用的配置（由后台服务生成，控制端口与密钥每次随机）。\n"
	_ = paths.WriteFileAtomic(c.d.Paths.LastConfigRedacted(), []byte(header+text), 0o600)
}

// MARK: 后台组件

// RepairService 以管理员身份重新安装并启动后台服务（会弹 UAC）。
func (c *Core) RepairService() {
	proceed := false
	c.change(func() {
		if !c.repairing {
			c.repairing = true
			proceed = true
		}
	})
	if !proceed {
		return
	}
	err := errors.New("不支持")
	if c.d.RepairService != nil {
		err = c.d.RepairService()
	}
	c.change(func() {
		c.repairing = false
		c.helloChecked = false
		c.triedStartingService = false
		if err == nil {
			c.serviceProblem = ""
		}
	})
	switch {
	case err == nil:
		c.ShowNotice("后台组件已修复", 3*time.Second)
		c.logf("info", "后台组件已修复")
	case errors.Is(err, svcctl.ErrCancelled):
		c.ShowNotice("已取消授权", 3*time.Second)
	default:
		c.ShowNotice("修复失败："+err.Error(), 6*time.Second)
		c.logf("error", "修复后台组件失败：%v", err)
	}
	c.poll(context.Background())
}

// MARK: 设置

func (c *Core) SetSetting(key string, value bool) {
	if key == "launchAtLogin" {
		if c.d.SetLaunchAtLogin == nil {
			return
		}
		if err := c.d.SetLaunchAtLogin(value); err != nil {
			c.ShowNotice("设置开机启动失败："+err.Error(), 5*time.Second)
			return
		}
		c.change(func() { c.launchAtLogin = value })
		return
	}
	c.updateSettings(func(s *settings.Settings) {
		switch key {
		case "strictRoute":
			s.StrictRoute = value
		case "autoConnectOnLaunch":
			s.AutoConnectOnLaunch = value
		case "autoCheckUpdates":
			s.AutoCheckUpdates = value
		}
	})
}

func (c *Core) updateSettings(f func(*settings.Settings)) {
	c.change(func() {
		next := c.settings
		f(&next)
		if err := c.d.Store.Save(next); err == nil {
			c.settings = next
		}
	})
}

func (c *Core) OpenCustomRules() {
	path := c.d.Paths.CustomRules()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_ = c.d.Paths.Prepare()
		_ = paths.WriteFileAtomic(path, []byte(config.CustomRulesTemplate), 0o600)
	}
	if c.d.OpenFile != nil {
		if err := c.d.OpenFile(path); err != nil {
			c.ShowNotice("打不开规则文件："+err.Error(), 5*time.Second)
		}
	}
}

func (c *Core) OpenLogsFolder() {
	if c.d.RevealFile == nil {
		return
	}
	target := paths.DefaultSystem().CoreLog()
	if _, err := os.Stat(target); err != nil {
		target = c.d.Paths.AppLog()
	}
	_ = c.d.RevealFile(target)
}

func (c *Core) OpenProjectPage() {
	if c.d.OpenURL != nil {
		_ = c.d.OpenURL(appinfo.ProjectURL)
	}
}

// MARK: 线路

func (c *Core) SelectNode(name string) {
	c.mu.Lock()
	api := c.api
	c.mu.Unlock()
	if api == nil {
		c.change(func() { c.selection = name })
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := api.Select(ctx, config.ProxyGroup, name); err != nil {
		c.ShowNotice("切换线路失败："+Message(err), 4*time.Second)
		return
	}
	_ = api.CloseAllConnections(ctx)
	c.change(func() {
		c.selection = name
		if name != AutoSelection {
			c.activeNode = name
		}
	})
}

func (c *Core) TestLatency() {
	var api *mihomo.Client
	var url string
	c.change(func() {
		if c.api != nil && !c.testing {
			c.testing = true
			api = c.api
			url = c.settings.HealthCheckURL
		}
	})
	if api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := api.GroupDelay(ctx, config.AutoGroup, url, 5000)
	c.change(func() {
		c.testing = false
		if err == nil {
			for i := range c.nodes {
				c.nodes[i].Delay = result[c.nodes[i].Name]
			}
			c.lastDelayRefresh = time.Now()
		}
	})
	if err != nil {
		c.ShowNotice("测速失败："+Message(err), 4*time.Second)
	}
}

// MARK: 诊断

func (c *Core) CopyDiagnostics(full bool) {
	proceed := false
	c.change(func() {
		if !c.copying {
			c.copying = true
			proceed = true
		}
	})
	if !proceed {
		return
	}
	c.ShowNotice("正在收集诊断信息…", 30*time.Second)
	info := c.DiagnosticsContext()
	text := ""
	if c.d.Diagnose != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		text = c.d.Diagnose(ctx, full, info)
		cancel()
	}
	var err error
	if c.d.SetClipboard != nil {
		err = c.d.SetClipboard(text)
	}
	c.change(func() { c.copying = false })
	if err != nil {
		c.ShowNotice("复制失败："+err.Error(), 5*time.Second)
		return
	}
	c.logf("info", "已复制诊断信息（%s，%d 字符）", map[bool]string{true: "完整版", false: "摘要版"}[full], len([]rune(text)))
	if full {
		c.ShowNotice("已复制完整诊断（含最近连接日志），直接粘贴给 AI 即可", 6*time.Second)
	} else {
		c.ShowNotice("已复制诊断信息，直接粘贴给 AI 即可", 6*time.Second)
	}
}

// DiagnosticsContext 汇总 App 自己知道的部分。
func (c *Core) DiagnosticsContext() DiagContext {
	phase := c.PhaseDescription()
	c.mu.Lock()
	info := DiagContext{AppPhase: phase, LastError: c.errorMessage, Settings: c.settings,
		Nodes: append([]NodeRow(nil), c.nodes...)}
	c.mu.Unlock()
	if rules, err := c.d.Store.LoadCustomRules(); err != nil {
		info.CustomRulesError = err.Error()
	} else {
		info.CustomRuleCount = len(rules)
	}
	return info
}

// MARK: 文案

// Message 把各种错误翻成给用户看的一句话。
func Message(err error) string {
	if err == nil {
		return ""
	}
	var ce *ipc.ClientError
	if errors.As(err, &ce) {
		if ce.Service != nil {
			switch ce.Service.Code {
			case "core-missing":
				return "内置组件不完整，请重新运行安装程序。"
			case "config-invalid":
				return "生成的配置没有通过校验。请点“遇到问题”复制诊断信息排查。"
			case "launch-failed":
				return "无法启动内置核心，可能被杀毒软件拦截了。请在杀毒软件里信任 MyTizi 的安装目录后重试。"
			case "tun-missing", "controller-timeout":
				return "没能启动虚拟网卡。请先退出其他代理/VPN 软件再试；仍不行请复制诊断信息。"
			case "bad-request":
				return ce.Service.Message
			default:
				return ce.Service.Message
			}
		}
		if ce.NotRunning {
			return "后台组件没有运行。点“修复后台组件”重新安装，或重新运行安装程序。"
		}
		return ce.Error()
	}
	var se *subscription.Error
	if errors.As(err, &se) {
		return se.Message
	}
	var he *mihomo.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("核心返回 %d", he.Status)
	}
	var te *mihomo.TransportError
	if errors.As(err, &te) {
		return "核心没有响应"
	}
	return strings.TrimSpace(err.Error())
}
