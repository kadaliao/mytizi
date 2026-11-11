package appcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/subscription"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
)

// fakeService 模拟后台服务：start 后报告 running，控制接口指向 fakeController。
type fakeService struct {
	mu          sync.Mutex
	installed   bool
	phase       ipc.Phase
	port        int
	lastStart   *ipc.StartParams
	updates     [][]byte
	startErr    *ipc.Error
	ipcVersion  int
	startCalls  int
	stopCalls   int
	helloCalled int
}

func (f *fakeService) notRunning() error {
	return &ipc.ClientError{NotRunning: true, Err: errors.New("no pipe")}
}

func (f *fakeService) Hello(context.Context) (*ipc.Hello, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.helloCalled++
	if !f.installed {
		return nil, f.notRunning()
	}
	return &ipc.Hello{IPCVersion: f.ipcVersion, CoreInstalled: true}, nil
}

func (f *fakeService) Status(context.Context) (*ipc.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.installed {
		return nil, f.notRunning()
	}
	return f.statusLocked(), nil
}

func (f *fakeService) statusLocked() *ipc.Status {
	st := &ipc.Status{Phase: f.phase}
	if f.phase == ipc.PhaseRunning {
		now := time.Now().Add(-5 * time.Minute)
		st.StartedAt = &now
		st.ControllerPort = f.port
		st.ControllerSecret = "secret-0123456789"
		st.TunInterface = "MyTizi"
	}
	return st
}

func (f *fakeService) Start(_ context.Context, p ipc.StartParams) (*ipc.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	f.lastStart = &p
	if f.startErr != nil {
		return nil, &ipc.ClientError{Service: f.startErr}
	}
	f.phase = ipc.PhaseRunning
	return f.statusLocked(), nil
}

func (f *fakeService) Stop(context.Context) (*ipc.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	f.phase = ipc.PhaseStopped
	return f.statusLocked(), nil
}

func (f *fakeService) UpdateSubscription(_ context.Context, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, body)
	return nil
}

func (f *fakeService) set(fn func(*fakeService)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

// fakeController 模拟 mihomo 控制接口。
type fakeController struct {
	mu       sync.Mutex
	delays   map[string]int
	selected string
	server   *httptest.Server
	closes   int
	reloads  int
}

func newController(t *testing.T) *fakeController {
	c := &fakeController{delays: map[string]int{"香港-01": 82, "日本-02": 120}, selected: "AUTO"}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-0123456789" {
			w.WriteHeader(401)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		path := r.URL.EscapedPath()
		switch {
		case r.Method == "GET" && path == "/proxies/PROXY":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "PROXY", "now": c.selected})
		case r.Method == "GET" && path == "/proxies/AUTO":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "AUTO", "now": "香港-01"})
		case r.Method == "PUT" && path == "/proxies/PROXY":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.selected = body["name"]
			w.WriteHeader(204)
		case r.Method == "GET" && path == "/providers/proxies/sub":
			var proxies []map[string]any
			for _, name := range []string{"香港-01", "日本-02"} {
				p := map[string]any{"name": name, "type": "Shadowsocks"}
				if d := c.delays[name]; d > 0 {
					p["history"] = []map[string]any{{"delay": d}}
				}
				proxies = append(proxies, p)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "sub", "proxies": proxies})
		case r.Method == "PUT" && path == "/providers/proxies/sub":
			c.reloads++
			w.WriteHeader(204)
		case r.Method == "GET" && path == "/group/AUTO/delay":
			result := map[string]int{}
			for k, v := range c.delays {
				if v > 0 {
					result[k] = v
				}
			}
			if len(result) == 0 {
				w.WriteHeader(504)
				_, _ = w.Write([]byte(`{"message":"Timeout"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(result)
		case r.Method == "DELETE" && path == "/connections":
			c.closes++
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *fakeController) port() int {
	_, p, _ := net.SplitHostPort(c.server.Listener.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

type harness struct {
	core       *Core
	service    *fakeService
	controller *fakeController
	store      settings.Store
	clipboard  string
	repairs    int
	repairErr  error
	state      svcctl.State
	states     []UIState
	mu         sync.Mutex
}

const sampleBody = "proxies:\n  - {name: 香港-01, type: ss, server: 203.0.113.1, port: 1, cipher: aes-128-gcm, password: x}\n"

func newHarness(t *testing.T, withSubscription bool) *harness {
	t.Helper()
	h := &harness{controller: newController(t), state: svcctl.Running}
	h.service = &fakeService{installed: true, phase: ipc.PhaseStopped, ipcVersion: 1, port: h.controller.port()}
	user := paths.UserAt(t.TempDir())
	h.store = settings.NewStore(user)
	if withSubscription {
		now := time.Now().Add(-2 * time.Hour)
		s := settings.Default()
		s.Subscription = &settings.Subscription{
			URL: "https://sub.example.com/api?token=abcdef123456", Name: "我的订阅", AddedAt: now, UpdatedAt: &now,
			Nodes: []settings.Node{{Name: "香港-01", Type: "Shadowsocks"}, {Name: "日本-02", Type: "Shadowsocks"}},
			Usage: &settings.Usage{Upload: 1 << 30, Download: 39 << 30, Total: 100 << 30},
		}
		if err := h.store.Save(s); err != nil {
			t.Fatal(err)
		}
		if err := h.store.SaveSubscriptionBody([]byte(sampleBody)); err != nil {
			t.Fatal(err)
		}
	}
	h.core = New(Deps{
		Client: h.service, Store: h.store, Paths: user, Version: "0.4.0",
		Probe: func(_ context.Context, body []byte) ([]settings.Node, error) {
			if strings.Contains(string(body), "garbage") {
				return nil, nil
			}
			return []settings.Node{{Name: "香港-01", Type: "Shadowsocks"}, {Name: "美国-03", Type: "Trojan"}}, nil
		},
		Fetch: func(_ context.Context, link string) (*subscription.Fetched, error) {
			if strings.Contains(link, "down") {
				return nil, &subscription.Error{Kind: "unreachable", Message: "连不上订阅服务器：连接超时"}
			}
			body := sampleBody
			if strings.Contains(link, "garbage") {
				body = "garbage"
			}
			return &subscription.Fetched{Body: []byte(body), Title: "公司订阅"}, nil
		},
		Diagnose: func(_ context.Context, full bool, info DiagContext) string {
			return "# 诊断 " + info.AppPhase
		},
		ServiceState: func() svcctl.State { h.mu.Lock(); defer h.mu.Unlock(); return h.state },
		StartService: func() error { return errors.New("拒绝访问") },
		RepairService: func() error {
			h.mu.Lock()
			h.repairs++
			err := h.repairErr
			h.mu.Unlock()
			if err == nil {
				h.service.set(func(f *fakeService) { f.installed = true; f.ipcVersion = 1 })
			}
			return err
		},
		LocalDNS:     func() []string { return []string{"192.168.1.1"} },
		Tailscale:    func() bool { return true },
		SetClipboard: func(s string) error { h.mu.Lock(); h.clipboard = s; h.mu.Unlock(); return nil },
		Emit:         func(s UIState) { h.mu.Lock(); h.states = append(h.states, s); h.mu.Unlock() },
	})
	t.Cleanup(h.core.Stop)
	return h
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestInitialStateWithoutSubscription(t *testing.T) {
	h := newHarness(t, false)
	s := h.core.State()
	if s.Phase != PhaseNeedsSubscription || s.Subscription != nil || s.Nodes == nil {
		t.Fatalf("got %+v", s)
	}
}

func TestConnectSendsValidatedParamsAndShowsConnected(t *testing.T) {
	h := newHarness(t, true)
	if err := h.store.Save(func() settings.Settings { s := h.store.Load(); return s }()); err != nil {
		t.Fatal(err)
	}
	h.core.Connect()
	s := h.core.State()
	if s.Phase != PhaseConnected || s.StartedAt == nil {
		t.Fatalf("应当已连接：%+v", s)
	}
	p := h.service.lastStart
	if p == nil || string(p.Subscription) != sampleBody || !p.StrictRoute || !p.TailscaleDNS ||
		len(p.DirectHosts) != 1 || p.DirectHosts[0] != "sub.example.com" || p.LocalDNS[0] != "192.168.1.1" {
		t.Fatalf("start 参数不对：%+v", p)
	}
	if s.ActiveNode != "香港-01" || s.Selection != AutoSelection {
		t.Errorf("应读出当前线路：%+v", s)
	}
	if s.Nodes[0].Delay != 82 {
		t.Errorf("应读出延迟：%+v", s.Nodes)
	}
	if s.Subscription == nil || s.Subscription.Usage == nil || s.Subscription.Usage.UsedText != "40.00 GB" ||
		s.Subscription.UpdatedText != "2小时前更新" {
		t.Errorf("订阅卡片数据不对：%+v", s.Subscription)
	}
	// 断开
	h.core.Toggle()
	if s := h.core.State(); s.Phase != PhaseDisconnected || s.ActiveNode != "" {
		t.Fatalf("应当已断开：%+v", s)
	}
}

func TestStartFailureShowsFriendlyMessage(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) {
		f.startErr = &ipc.Error{Code: "tun-missing", Message: "没能创建虚拟网卡（TUN）。"}
	})
	h.core.Connect()
	s := h.core.State()
	if s.Phase != PhaseFailed || !strings.Contains(s.ErrorMessage, "退出其他代理") {
		t.Fatalf("got %+v", s)
	}
	// 失败后可以重试
	h.service.set(func(f *fakeService) { f.startErr = nil })
	h.core.Toggle()
	if h.core.State().Phase != PhaseConnected {
		t.Fatal("重试应当成功")
	}
}

func TestServiceMissingTriggersRepairBeforeConnecting(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) { f.installed = false })
	h.state = svcctl.NotInstalled
	h.core.Connect()
	if h.repairs != 1 || h.core.State().Phase != PhaseConnected {
		t.Fatalf("应先修复再连接：repairs=%d state=%+v", h.repairs, h.core.State())
	}
}

func TestCancelledUACReturnsToDisconnected(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) { f.installed = false })
	h.state = svcctl.NotInstalled
	h.repairErr = svcctl.ErrCancelled
	h.core.Connect()
	s := h.core.State()
	if s.Phase != PhaseDisconnected || !strings.Contains(s.Notice, "取消") {
		t.Fatalf("got %+v", s)
	}
}

func TestPollAdoptsRunningServiceAndDetectsCrash(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) { f.phase = ipc.PhaseRunning })
	h.core.poll(context.Background())
	if h.core.State().Phase != PhaseConnected {
		t.Fatal("App 重新打开时应接管已有连接")
	}
	h.service.set(func(f *fakeService) { f.phase = ipc.PhaseCrashed })
	h.core.poll(context.Background())
	s := h.core.State()
	if s.Phase != PhaseFailed || !strings.Contains(s.ErrorMessage, "意外中断") {
		t.Fatalf("应报告崩溃：%+v", s)
	}
}

func TestOldCrashOnLaunchDoesNotAlarm(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) { f.phase = ipc.PhaseCrashed })
	h.core.poll(context.Background())
	if s := h.core.State(); s.Phase != PhaseDisconnected || s.ErrorMessage != "" {
		t.Fatalf("启动时读到旧的崩溃记录不应打扰用户：%+v", s)
	}
}

func TestServiceProblemIsReported(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) { f.installed = false })
	h.state = svcctl.NotInstalled
	h.core.poll(context.Background())
	if !strings.Contains(h.core.State().ServiceProblem, "没有安装") {
		t.Fatalf("got %+v", h.core.State().ServiceProblem)
	}
	h.core.RepairService()
	if s := h.core.State(); s.ServiceProblem != "" || s.RepairingService {
		t.Fatalf("修复后应清除：%+v", s)
	}
}

func TestIPCVersionMismatch(t *testing.T) {
	h := newHarness(t, true)
	h.service.set(func(f *fakeService) { f.ipcVersion = 99 })
	h.core.poll(context.Background())
	if !strings.Contains(h.core.State().ServiceProblem, "版本不匹配") {
		t.Fatalf("got %q", h.core.State().ServiceProblem)
	}
}

func TestAddSubscription(t *testing.T) {
	h := newHarness(t, false)
	h.core.AddSubscription("  https://sub.example.com/x?token=abcdef123456\n")
	s := h.core.State()
	if s.Phase != PhaseDisconnected || s.Subscription == nil || s.Subscription.Name != "公司订阅" || len(s.Nodes) != 2 || s.AddError != "" {
		t.Fatalf("got %+v", s)
	}
	body, _ := h.store.LoadSubscriptionBody()
	if string(body) != sampleBody {
		t.Error("订阅原文没有保存")
	}
}

func TestAddSubscriptionErrors(t *testing.T) {
	h := newHarness(t, false)
	cases := map[string]string{
		"not a link":                      "不是有效的订阅链接",
		"https://down.example.com/s":      "连不上订阅服务器",
		"https://garbage.example.com/sub": "没有找到可用的线路",
	}
	for link, want := range cases {
		h.core.AddSubscription(link)
		if s := h.core.State(); !strings.Contains(s.AddError, want) || s.Phase != PhaseNeedsSubscription || s.AddingSubscription {
			t.Errorf("%s：got %+v", link, s)
		}
	}
	// 日志里不能出现订阅 token
	h.core.AddSubscription("https://down.example.com/s?token=supersecret999")
	data := h.core.log.Tail(50)
	if strings.Contains(data, "supersecret999") {
		t.Fatalf("app.log 泄漏了订阅 token：%s", data)
	}
}

func TestRefreshWhileConnectedPushesToService(t *testing.T) {
	h := newHarness(t, true)
	h.core.Connect()
	h.core.RefreshSubscription()
	if len(h.service.updates) != 1 || h.controller.reloads != 1 {
		t.Fatalf("已连接时刷新应推给服务并让核心重载：updates=%d reloads=%d", len(h.service.updates), h.controller.reloads)
	}
	s := h.core.State()
	if s.Subscription.Name != "公司订阅" || s.Subscription.NodeCount != 2 || !strings.Contains(s.Notice, "订阅已更新") {
		t.Fatalf("got %+v", s)
	}
}

func TestSelectNodeAndLatency(t *testing.T) {
	h := newHarness(t, true)
	h.core.Connect()
	h.core.SelectNode("日本-02")
	if s := h.core.State(); s.Selection != "日本-02" || s.ActiveNode != "日本-02" || h.controller.closes == 0 {
		t.Fatalf("got %+v", s)
	}
	h.core.TestLatency()
	if s := h.core.State(); s.TestingLatency || s.Nodes[1].Delay != 120 {
		t.Fatalf("got %+v", s.Nodes)
	}
}

func TestAllNodesDownIsDetected(t *testing.T) {
	h := newHarness(t, true)
	h.controller.mu.Lock()
	h.controller.delays = map[string]int{}
	h.controller.mu.Unlock()
	h.core.Connect()
	waitFor(t, "线路全挂的提示", func() bool { return h.core.State().ConnectivityIssue != "" })
	h.controller.mu.Lock()
	h.controller.delays = map[string]int{"香港-01": 50}
	h.controller.mu.Unlock()
	h.core.mu.Lock()
	h.core.lastDelayRefresh = time.Time{}
	h.core.mu.Unlock()
	h.core.poll(context.Background())
	waitFor(t, "提示消失", func() bool { return h.core.State().ConnectivityIssue == "" })
}

func TestCopyDiagnostics(t *testing.T) {
	h := newHarness(t, true)
	h.core.CopyDiagnostics(false)
	if !strings.HasPrefix(h.clipboard, "# 诊断 未连接") {
		t.Fatalf("clipboard = %q", h.clipboard)
	}
	if s := h.core.State(); s.CopyingDiagnostics || !strings.Contains(s.Notice, "已复制") {
		t.Fatalf("got %+v", s)
	}
}

func TestSettingsPersist(t *testing.T) {
	h := newHarness(t, true)
	h.core.SetSetting("strictRoute", false)
	h.core.SetSetting("autoConnectOnLaunch", true)
	if s := h.store.Load(); s.StrictRoute || !s.AutoConnectOnLaunch {
		t.Fatalf("got %+v", s)
	}
	h.core.Connect()
	if h.service.lastStart.StrictRoute {
		t.Error("关闭严格接管后连接参数应跟着变")
	}
}

func TestRemoveSubscriptionDisconnects(t *testing.T) {
	h := newHarness(t, true)
	h.core.Connect()
	h.core.RemoveSubscription()
	s := h.core.State()
	if s.Phase != PhaseNeedsSubscription || s.Subscription != nil || h.service.stopCalls != 1 {
		t.Fatalf("got %+v stop=%d", s, h.service.stopCalls)
	}
}

func TestNoticeClearsItself(t *testing.T) {
	h := newHarness(t, true)
	h.core.ShowNotice("你好", 50*time.Millisecond)
	if h.core.State().Notice != "你好" {
		t.Fatal("应显示提示")
	}
	waitFor(t, "提示自动消失", func() bool { return h.core.State().Notice == "" })
}

func TestFormatting(t *testing.T) {
	if got := Bytes(43003150336); got != "40.05 GB" {
		t.Errorf("Bytes = %s", got)
	}
	if got := Bytes(199990000000); got != "186.3 GB" {
		t.Errorf("Bytes = %s", got)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "刚刚更新", 5 * time.Minute: "5分钟前更新", 3 * time.Hour: "3小时前更新",
		50 * time.Hour: "2天前更新", 15 * 24 * time.Hour: "2周前更新",
	} {
		t0 := now.Add(-d)
		if got := UpdatedText(&t0, now); got != want {
			t.Errorf("%v: %s, want %s", d, got, want)
		}
	}
	if DateText(time.Date(2027, 1, 18, 9, 0, 0, 0, time.Local)) != "2027年1月18日" {
		t.Error("DateText")
	}
}
