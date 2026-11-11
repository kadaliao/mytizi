package service

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/mihomo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/proc"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/testutil"
)

type countingPlatform struct {
	DefaultPlatform
	stops atomic.Int32
}

func (p *countingPlatform) AfterCoreStopped() { p.stops.Add(1) }

// newSupervisor 用临时目录模拟安装目录与系统目录：核心与地理库来自 Vendor/core。
func newSupervisor(t *testing.T) (*Supervisor, *countingPlatform, paths.System) {
	t.Helper()
	core := testutil.RequireCore(t)
	install := t.TempDir()
	components := paths.ComponentsIn(install)
	if err := os.MkdirAll(components.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(src, dst string) {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link(core, components.Core())
	link(filepath.Join(testutil.VendorDir(), "geoip.metadb"), components.GeoIP())
	link(filepath.Join(testutil.VendorDir(), "geosite.dat"), components.GeoSite())

	sys := paths.SystemAt(t.TempDir())
	platform := &countingPlatform{}
	log, coreLog := rotlog.New(sys.HelperLog(), 0, 0), rotlog.New(sys.CoreLog(), 0, 0)
	// 清理按注册的逆序执行：先停核心，再关日志，最后删临时目录（Windows 删不掉打开着的文件）。
	t.Cleanup(func() { log.Close(); coreLog.Close() })
	s := New(Options{
		System: sys, Components: components, TUN: false, Platform: platform, Log: log, CoreLog: coreLog,
	})
	t.Cleanup(s.Shutdown)
	return s, platform, sys
}

func params() ipc.StartParams {
	return ipc.StartParams{
		Subscription: []byte(testutil.SampleClashSubscription),
		DirectHosts:  []string{"sub.example.com"},
		CustomRules:  []string{"DOMAIN-SUFFIX,corp.example,DIRECT"},
		LocalDNS:     []string{"192.168.1.1"},
		StrictRoute:  true,
	}
}

func TestStartStopLifecycle(t *testing.T) {
	s, platform, sys := newSupervisor(t)
	r := s.Start(params())
	if !r.OK {
		t.Fatalf("start 失败：%+v %+v", r.Error, s.opt.CoreLog.Tail(20))
	}
	st := r.Status
	if st.Phase != ipc.PhaseRunning || st.CorePID == 0 || st.ControllerPort == 0 || len(st.ControllerSecret) < 32 || st.StartedAt == nil {
		t.Fatalf("状态不对：%+v", st)
	}
	// 配置由服务生成，订阅原文落在 providers/sub.yaml，地理库已复制。
	config, _ := os.ReadFile(sys.ConfigPath())
	for _, want := range []string{"DOMAIN,sub.example.com,DIRECT", "DOMAIN-SUFFIX,corp.example,DIRECT", "strict-route: true", "\"+.lan\""} {
		if !strings.Contains(string(config), want) {
			t.Errorf("配置里缺少 %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(sys.CoreHome, "geoip.metadb")); err != nil {
		t.Error("地理库没有复制到工作目录")
	}
	// 控制接口可用，节点来自订阅。
	api := mihomo.New(st.ControllerPort, st.ControllerSecret)
	provider, err := api.Provider(context.Background(), "sub")
	if err != nil || len(provider.Proxies) != 2 {
		t.Fatalf("provider = %+v, %v", provider, err)
	}

	stopped := s.Stop()
	if !stopped.OK || stopped.Status.Phase != ipc.PhaseStopped || stopped.Status.ControllerSecret != "" {
		t.Fatalf("stop 后状态不对：%+v", stopped.Status)
	}
	if platform.stops.Load() == 0 {
		t.Error("核心停止后应清空 DNS 缓存")
	}
	if last := s.Status().LastExit; last == nil || !last.Expected {
		t.Errorf("预期内退出应被记录：%+v", last)
	}
	if _, err := api.Version(context.Background()); err == nil {
		t.Error("停止后控制接口不应再响应")
	}
}

// 控制端口在探测之后被别人抢走：核心只在日志里报错、不退出。服务应当换个端口重试，而不是干等超时。
func TestControllerPortTakenIsRetried(t *testing.T) {
	s, _, sys := newSupervisor(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	first := true
	s.opt.PickPort = func() int {
		if first {
			first = false
			return taken.Addr().(*net.TCPAddr).Port
		}
		return proc.FreePort()
	}
	started := time.Now()
	r := s.Start(params())
	if !r.OK {
		t.Fatalf("应当换端口后连接成功：%+v", r.Error)
	}
	if r.Status.ControllerPort == taken.Addr().(*net.TCPAddr).Port {
		t.Error("应当换了一个端口")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("发现端口被占用应当立即重试，实际用了 %v", elapsed)
	}
	if log := rotlog.Tail(sys.HelperLog(), 20, 0); !strings.Contains(log, "被占用") {
		t.Errorf("服务日志里应记录重试：%s", log)
	}
}

func TestRestartWhileRunningReplacesCore(t *testing.T) {
	s, _, _ := newSupervisor(t)
	first := s.Start(params())
	if !first.OK {
		t.Fatal(first.Error)
	}
	second := s.Start(params())
	if !second.OK {
		t.Fatal(second.Error)
	}
	if first.Status.CorePID == second.Status.CorePID {
		t.Error("重新 start 应当换一个核心进程")
	}
	if first.Status.ControllerSecret == second.Status.ControllerSecret {
		t.Error("每次连接的控制密钥应当不同")
	}
	if s.Status().Phase != ipc.PhaseRunning {
		t.Error("旧核心退出不应把新连接标成崩溃")
	}
}

func TestCrashIsDetected(t *testing.T) {
	s, platform, _ := newSupervisor(t)
	r := s.Start(params())
	if !r.OK {
		t.Fatal(r.Error)
	}
	process, _ := os.FindProcess(r.Status.CorePID)
	before := platform.stops.Load()
	_ = process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().Phase != ipc.PhaseCrashed && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	st := s.Status()
	if st.Phase != ipc.PhaseCrashed || st.LastExit == nil || st.LastExit.Expected || st.ControllerSecret != "" {
		t.Fatalf("核心被杀后应报告崩溃：%+v", st)
	}
	if platform.stops.Load() == before {
		t.Error("崩溃后也应清空 DNS 缓存")
	}
	// 崩溃后可以直接重新连接。
	if again := s.Start(params()); !again.OK {
		t.Fatal(again.Error)
	}
}

func TestInvalidParamsNeverReachTheCore(t *testing.T) {
	s, _, sys := newSupervisor(t)
	bad := []func(*ipc.StartParams){
		func(p *ipc.StartParams) { p.Subscription = nil },
		func(p *ipc.StartParams) { p.Subscription = []byte("<html>login</html>") },
		func(p *ipc.StartParams) { p.CustomRules = []string{"DOMAIN,x,DIRECT\nMATCH,DIRECT"} },
		func(p *ipc.StartParams) { p.CustomRules = []string{"RULE-SET,x,PROXY"} },
		func(p *ipc.StartParams) { p.CustomRules = []string{"# 注释"} },
		func(p *ipc.StartParams) { p.DirectHosts = []string{"evil\"\nexternal-ui: C:\\"} },
		func(p *ipc.StartParams) { p.HealthCheckURL = "file:///C:/Windows/win.ini" },
		func(p *ipc.StartParams) { p.HealthCheckURL = "https://x.example/\"" },
		func(p *ipc.StartParams) { p.LocalDNS = []string{"192.168.1.1\nexternal-controller: 0.0.0.0:9090"} },
		func(p *ipc.StartParams) { p.LocalDNS = []string{"fe80::1"} },
	}
	for i, mutate := range bad {
		p := params()
		mutate(&p)
		r := s.Start(p)
		if r.OK || r.Error.Code != "bad-request" {
			t.Errorf("第 %d 组非法参数应被拒绝：%+v", i, r)
		}
	}
	if _, err := os.Stat(sys.ConfigPath()); err == nil {
		t.Error("参数非法时不应写出配置")
	}
	if s.Status().Phase != ipc.PhaseStopped {
		t.Error("参数非法时不应改变状态")
	}
}

func TestMissingCoreIsReported(t *testing.T) {
	s, _, _ := newSupervisor(t)
	_ = os.Remove(s.opt.Components.Core())
	r := s.Start(params())
	if r.OK || r.Error.Code != "core-missing" {
		t.Fatalf("got %+v", r)
	}
}

func TestUpdateSubscriptionOnlyRewritesTheFile(t *testing.T) {
	s, _, sys := newSupervisor(t)
	r := s.Start(params())
	if !r.OK {
		t.Fatal(r.Error)
	}
	pid := r.Status.CorePID
	updated := "proxies:\n  - {name: \"新节点\", type: http, server: 192.0.2.9, port: 8080}\n"
	if u := s.UpdateSubscription([]byte(updated)); !u.OK {
		t.Fatal(u.Error)
	}
	data, _ := os.ReadFile(filepath.Join(sys.CoreHome, "providers", "sub.yaml"))
	if string(data) != updated {
		t.Error("订阅文件没有更新")
	}
	if s.Status().CorePID != pid {
		t.Error("刷新订阅不应重启核心")
	}
	if u := s.UpdateSubscription([]byte("<!doctype html>")); u.OK {
		t.Error("网页内容应被拒绝")
	}
}

// 管道协议：通过真实的 listener 走一遍一问一答。
func TestServeOverListener(t *testing.T) {
	s, _, _ := newSupervisor(t)
	dir, err := os.MkdirTemp("", "mt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("当前平台不支持 unix socket：", err)
	}
	defer listener.Close()
	go ipc.Serve(listener, s.Handle, func(string, ...any) {})
	client := &ipc.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	hello, err := client.Hello(context.Background())
	if err != nil || ipc.CheckHello(hello) != nil || !hello.CoreInstalled {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	st, err := client.Start(context.Background(), params())
	if err != nil || st.Phase != ipc.PhaseRunning {
		t.Fatalf("start = %+v, %v", st, err)
	}
	if err := client.UpdateSubscription(context.Background(), []byte(testutil.SampleClashSubscription)); err != nil {
		t.Fatal(err)
	}
	_, err = client.Start(context.Background(), ipc.StartParams{})
	var ce *ipc.ClientError
	if err == nil || !asClientError(err, &ce) || ce.Service == nil || ce.Service.Code != "bad-request" {
		t.Fatalf("空参数应返回 bad-request，得到 %v", err)
	}
	if st, err := client.Stop(context.Background()); err != nil || st.Phase != ipc.PhaseStopped {
		t.Fatalf("stop = %+v, %v", st, err)
	}
	listener.Close()
	if _, err := client.Status(context.Background()); err == nil || !asClientError(err, &ce) || !ce.NotRunning {
		t.Fatalf("服务不在时应报 NotRunning，得到 %v", err)
	}
}

func asClientError(err error, target **ipc.ClientError) bool {
	ce, ok := err.(*ipc.ClientError)
	if ok {
		*target = ce
	}
	return ok
}
