package diag

import (
	"strings"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
)

var testNow = time.Unix(1_790_000_000, 0)

func ptr[T any](v T) *T { return &v }

func wifi() Adapter {
	return Adapter{Name: "WLAN", Description: "Intel(R) Wi-Fi 6E AX211 160MHz", IfType: IfTypeIEEE80211, Up: true,
		IPv4: []string{"192.168.2.205"}, DNS: []string{"192.168.2.1"}, Gateways: []string{"192.168.2.1"}, Metric: 35}
}

func ourTUN() Adapter {
	return Adapter{Name: appinfo.TunDevice, Description: "MyTizi Tunnel", IfType: IfTypePropVirtual, Up: true,
		IPv4: []string{appinfo.TunGateway}, DNS: []string{appinfo.TunDNS}}
}

func healthyInput() Input {
	in := Input{Now: testNow, AppVersion: "0.4.0", ServiceState: svcctl.Running}
	in.Settings = settings.Default()
	in.Settings.Subscription = &settings.Subscription{
		URL: "https://sub.example.com/x?token=abcdef123456", Name: "公司", AddedAt: testNow, UpdatedAt: ptr(testNow),
		Nodes: []settings.Node{{Name: "HK", Type: "Shadowsocks"}},
	}
	in.Hello = &ipc.Hello{IPCVersion: appinfo.IPCVersion, ServicePID: 10, ServiceVersion: "0.4.0", CoreInstalled: true, InstallDir: `C:\Program Files\MyTizi`}
	in.Status = &ipc.Status{Phase: ipc.PhaseRunning, CorePID: 99, StartedAt: ptr(testNow), ControllerPort: 19097,
		ControllerSecret: "s", TunInterface: appinfo.TunDevice, StrictRoute: true}
	in.Facts = Facts{
		Adapters: []Adapter{wifi(), ourTUN()},
		DefaultRoutes: []DefaultRoute{
			{Destination: "0.0.0.0", PrefixLen: 1, Gateway: "198.18.0.2", InterfaceIP: appinfo.TunGateway, Adapter: appinfo.TunDevice},
			{Destination: "0.0.0.0", Gateway: "192.168.2.1", InterfaceIP: "192.168.2.205", Adapter: "WLAN", Metric: 35},
		},
	}
	in.Nodes = []NodeDelay{{Name: "HK", Type: "Shadowsocks", Delay: 80}}
	return in
}

func titles(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Title)
	}
	return out
}

func has(findings []Finding, title string) *Finding {
	for i := range findings {
		if findings[i].Title == title {
			return &findings[i]
		}
	}
	return nil
}

func TestHealthyStateHasNoFindings(t *testing.T) {
	if f := Findings(healthyInput()); len(f) != 0 {
		t.Fatalf("健康状态不应有结论：%v", titles(f))
	}
}

func TestServiceNotInstalledIsReportedOnce(t *testing.T) {
	in := healthyInput()
	in.ServiceState = svcctl.NotInstalled
	in.Hello, in.Status = nil, nil
	in.ServiceError = "后台组件没有运行。"
	got := titles(Findings(in))
	if len(got) != 1 || got[0] != "后台组件没有安装" {
		t.Fatalf("没装时只报一条：%v", got)
	}
}

func TestServiceStoppedAndUnreachable(t *testing.T) {
	in := healthyInput()
	in.ServiceState = svcctl.Stopped
	in.Hello, in.Status = nil, nil
	in.ServiceError = "后台组件没有运行。"
	f := Findings(in)
	if f[0].Title != "后台组件没有运行" || !strings.Contains(f[0].Detail, "sc.exe start MyTiziHelper") {
		t.Fatalf("got %+v", f[0])
	}
	in.ServiceState = svcctl.Running
	in.ServiceError = "与后台组件通信失败：timeout"
	f = Findings(in)
	if f[0].Title != "无法连接后台组件" || !strings.Contains(f[0].Detail, appinfo.PipeName) {
		t.Fatalf("got %+v", f[0])
	}
}

func TestVersionMismatchAndMissingCore(t *testing.T) {
	in := healthyInput()
	in.Hello.IPCVersion = appinfo.IPCVersion + 1
	in.Hello.CoreInstalled = false
	in.Hello.ServiceVersion = "0.3.9"
	f := Findings(in)
	for _, title := range []string{"后台组件版本不匹配", "内置核心缺失", "App 与后台组件的版本不同"} {
		if has(f, title) == nil {
			t.Errorf("缺少 %s：%v", title, titles(f))
		}
	}
}

func TestMissingTUNAndRivalTUN(t *testing.T) {
	in := healthyInput()
	in.Facts.Adapters = []Adapter{wifi(), {Name: "Mihomo", Description: "Meta Tunnel", IfType: IfTypePropVirtual, Up: true, IPv4: []string{"198.18.0.9"}}}
	got := titles(Findings(in))
	for _, title := range []string{"找不到 TUN 网卡", "检测到另一个 TUN 在使用同一地址段"} {
		if !strings.Contains(strings.Join(got, "|"), title) {
			t.Errorf("缺少 %s：%v", title, got)
		}
	}
}

func TestTUNDNSNotPointingAtMihomo(t *testing.T) {
	in := healthyInput()
	tun := ourTUN()
	tun.DNS = nil
	in.Facts.Adapters = []Adapter{wifi(), tun}
	f := Findings(in)
	if len(f) != 1 || f[0].Severity != Problem || !strings.Contains(f[0].Title, appinfo.TunDNS) {
		t.Fatalf("got %+v", f)
	}
}

func TestTailscaleCoexistingWithoutExitNodeIsSilent(t *testing.T) {
	in := healthyInput()
	in.Facts.Adapters = append(in.Facts.Adapters, Adapter{Name: "Tailscale", Description: "Tailscale Tunnel", IfType: IfTypePropVirtual, Up: true, IPv4: []string{"100.101.102.103"}})
	in.Facts.TailscaleRunning = true
	if f := Findings(in); len(f) != 0 {
		t.Fatalf("Tailscale 只接管自己的地址段时不是问题：%v", titles(f))
	}
}

func TestTailscaleExitNodeIsAWarning(t *testing.T) {
	in := healthyInput()
	in.Facts.Adapters = append(in.Facts.Adapters, Adapter{Name: "Tailscale", Description: "Tailscale Tunnel", IfType: IfTypePropVirtual, Up: true, IPv4: []string{"100.101.102.103"}})
	in.Facts.DefaultRoutes = append([]DefaultRoute{in.Facts.DefaultRoutes[0],
		{Destination: "0.0.0.0", InterfaceIP: "100.101.102.103", Adapter: "Tailscale", Metric: 5}}, in.Facts.DefaultRoutes[1:]...)
	f := Findings(in)
	if len(f) != 1 || f[0].Severity != Warning || !strings.Contains(f[0].Title, "exit node") {
		t.Fatalf("got %+v", f)
	}
}

func TestTAPStyleVPNOwningDefaultRouteIsAProblem(t *testing.T) {
	in := healthyInput()
	in.Facts.Adapters = append(in.Facts.Adapters, Adapter{Name: "以太网 2", Description: "TAP-Windows Adapter V9", IfType: IfTypeEthernet, Up: true, IPv4: []string{"10.8.0.6"}})
	in.Facts.DefaultRoutes = append(in.Facts.DefaultRoutes[:1],
		DefaultRoute{Destination: "0.0.0.0", InterfaceIP: "10.8.0.6", Adapter: "以太网 2", Metric: 20}, in.Facts.DefaultRoutes[1])
	f := Findings(in)
	if len(f) != 1 || f[0].Severity != Problem || !strings.Contains(f[0].Title, "另一个 VPN（以太网 2）") {
		t.Fatalf("got %+v", f)
	}
}

func TestStrictRouteOffWarnsAboutDNSAndIPv6(t *testing.T) {
	in := healthyInput()
	in.Status.StrictRoute = false
	w := wifi()
	w.IPv6 = []string{"240e:306:289a:400::5"}
	in.Facts.Adapters[0] = w
	f := Findings(in)
	if len(f) != 1 || f[0].Severity != Warning || !strings.Contains(f[0].Detail, "WLAN") || !strings.Contains(f[0].Title, "strict-route") {
		t.Fatalf("got %+v", f)
	}
}

func TestNRPTRulesUnderStrictRoute(t *testing.T) {
	in := healthyInput()
	in.Facts.NRPT = []NRPTRule{{Namespace: []string{".tail1234.ts.net"}, NameServers: []string{"100.100.100.100"}}}
	f := has(Findings(in), "系统里有按域名指定 DNS 的策略（NRPT）")
	if f == nil || !strings.Contains(f.Detail, "MagicDNS") {
		t.Fatalf("got %+v", f)
	}
	in.Facts.NRPT = []NRPTRule{{Namespace: []string{"corp.example.com"}, NameServers: []string{"10.0.0.53"}}}
	f = has(Findings(in), "系统里有按域名指定 DNS 的策略（NRPT）")
	if f == nil || !strings.Contains(f.Detail, "corp.example.com") || !strings.Contains(f.Detail, "DIRECT") {
		t.Fatalf("got %+v", f)
	}
}

func TestSystemProxy(t *testing.T) {
	in := healthyInput()
	in.Facts.Proxy = ProxyInfo{Enabled: true, Server: "127.0.0.1:7890"}
	f := Findings(in)
	if len(f) != 1 || f[0].Title != "系统代理仍然开启" || f[0].Severity != Warning {
		t.Fatalf("got %+v", f)
	}
	// 指向已关闭的端口：不论是否连接都是问题。
	in.Status = &ipc.Status{Phase: ipc.PhaseStopped}
	in.Facts.Adapters = []Adapter{wifi()}
	in.Facts.Proxy.DeadEndpoints = []string{"127.0.0.1:7890"}
	f = Findings(in)
	if len(f) != 1 || f[0].Severity != Problem || !strings.Contains(f[0].Detail, "127.0.0.1:7890") {
		t.Fatalf("got %+v", f)
	}
}

func TestStaleTUNAfterDisconnect(t *testing.T) {
	in := healthyInput()
	in.Status = &ipc.Status{Phase: ipc.PhaseStopped}
	f := Findings(in)
	if len(f) != 1 || f[0].Severity != Problem || !strings.Contains(f[0].Detail, "ipconfig /flushdns") {
		t.Fatalf("got %+v", f)
	}
	in.Facts.Adapters = []Adapter{wifi()}
	if f := Findings(in); len(f) != 0 {
		t.Fatalf("网卡已消失时没有问题：%v", titles(f))
	}
}

func TestSubscriptionProblems(t *testing.T) {
	in := healthyInput()
	in.Settings.Subscription.Usage = &settings.Usage{Download: 2000, Total: 1000, Expire: ptr(testNow.Add(-24 * time.Hour))}
	in.Settings.Subscription.UpdatedAt = ptr(testNow.Add(-9 * 24 * time.Hour))
	got := strings.Join(titles(Findings(in)), "|")
	for _, title := range []string{"订阅已过期", "订阅流量已用完", "订阅超过 7 天没有更新"} {
		if !strings.Contains(got, title) {
			t.Errorf("缺少 %s：%s", title, got)
		}
	}
	in.Settings.Subscription = nil
	if has(Findings(in), "还没有添加订阅") == nil {
		t.Error("没有订阅时应报出")
	}
	in = healthyInput()
	in.CustomRulesError = "第 2 行：不支持的规则类型 RULE-SET"
	if has(Findings(in), "自定义规则有错误（已被忽略）") == nil {
		t.Error("自定义规则错误应报出")
	}
}

func TestAllNodesDownAndCrash(t *testing.T) {
	in := healthyInput()
	in.Nodes = []NodeDelay{{Name: "a", Type: "Http"}, {Name: "b", Type: "Http"}}
	if has(Findings(in), "所有线路测速都失败") == nil {
		t.Error("线路全挂应报出")
	}
	in.Status = &ipc.Status{Phase: ipc.PhaseCrashed, LastExit: &ipc.CoreExit{At: testNow, Code: 2}}
	in.Facts.Adapters = []Adapter{wifi()}
	f := has(Findings(in), "核心意外退出")
	if f == nil || !strings.Contains(f.Detail, "退出码 2") {
		t.Fatalf("got %+v", f)
	}
}

func selfTestWithProxyDown() []SelfTestResult {
	return []SelfTestResult{
		{Name: "国内网站", URL: "https://www.baidu.com/", Expected: RouteDirect, Reachable: true, Status: 200, Milliseconds: 74,
			Observed: "DomainSuffix(baidu.com) → DIRECT", ObservedRoute: RouteDirect},
		{Name: "海外（google）", URL: "https://www.google.com/generate_204", Expected: RouteProxy, Milliseconds: 8001, Error: "timeout"},
	}
}

func TestNodeServerUnreachableIsDistinguished(t *testing.T) {
	in := healthyInput()
	in.Nodes = []NodeDelay{{Name: "Mac-FRP", Type: "Http"}}
	in.SelfTest = selfTestWithProxyDown()
	in.CoreLog = strings.Repeat(`level=warning msg="[TCP] dial PROXY (match Match/) 198.18.0.1:1 --> api.example.com:443 error: context deadline exceeded"`+"\n", 6)
	f := Findings(in)
	found := has(f, "节点服务器连不上（直连正常）")
	if found == nil || found.Severity != Problem || !strings.Contains(found.Detail, "context deadline exceeded") {
		t.Fatalf("got %v", titles(f))
	}
	if has(f, "所有线路测速都失败") != nil {
		t.Error("有更精确的结论时不再重复报泛化版本")
	}
}

func TestRefusedNodePortAndHandshake(t *testing.T) {
	in := healthyInput()
	in.Nodes = []NodeDelay{{Name: "Mac-FRP", Type: "Http"}}
	in.SelfTest = selfTestWithProxyDown()
	in.CoreLog = strings.Repeat(`level=warning msg="[TCP] dial PROXY (match Match/) 198.18.0.1:1 --> a.com:443 error: dial tcp 203.0.113.9:7890: connectex: No connection could be made because the target machine actively refused it."`+"\n", 4)
	if has(Findings(in), "节点端口没有服务（直连正常）") == nil {
		t.Error("Windows 的 actively refused 也应识别为端口没有服务")
	}
	in.CoreLog = strings.Repeat(`level=warning msg="[TCP] dial PROXY (match Match/) 198.18.0.1:1 --> a.com:443 error: tls: handshake failure"`+"\n", 3)
	if has(Findings(in), "节点可达但握手/认证失败（直连正常）") == nil {
		t.Error("握手失败应单独报出")
	}
}

func TestSelfTestFindings(t *testing.T) {
	in := healthyInput()
	in.SelfTest = []SelfTestResult{
		{Name: "国内 CDN", URL: "https://qpic.cn/", Expected: RouteDirect, Reachable: true, Observed: "Match → PROXY[x]", ObservedRoute: RouteProxy},
		{Name: "海外", URL: "https://www.google.com/", Expected: RouteProxy, Error: "timeout"},
		{Name: "IPv6 是否被拦截", URL: "tcp " + IPv6Target, Reachable: true, Concern: "IPv6 连接没有被拦截"},
	}
	f := Findings(in)
	if g := has(f, "自检：国内 CDN 的走向和预期不同"); g == nil || g.Severity != Warning {
		t.Errorf("走向不符应为注意：%v", titles(f))
	}
	if g := has(f, "自检失败：海外 不通"); g == nil || g.Severity != Problem {
		t.Errorf("不通应为问题：%v", titles(f))
	}
	if g := has(f, "自检失败：IPv6 是否被拦截"); g == nil || g.Severity != Problem {
		t.Errorf("IPv6 泄漏应为问题：%v", titles(f))
	}
}

func TestLogSignaturesAndConflictsAndGeo(t *testing.T) {
	in := healthyInput()
	in.CoreLog = strings.Repeat(`level=error msg="dial tcp: lookup x: no such host"`+"\n", 5)
	in.Facts.OtherProxyProcesses = []string{"202 clash-verge.exe"}
	in.GeoDate = testNow.Add(-100 * 24 * time.Hour)
	f := Findings(in)
	if g := has(f, "核心日志里 “no such host” 出现 5 次"); g == nil || g.Severity != Info {
		t.Errorf("日志签名：%v", titles(f))
	}
	if g := has(f, "检测到其他代理/VPN 进程"); g == nil || !strings.Contains(g.Detail, "clash-verge") {
		t.Errorf("冲突进程：%v", titles(f))
	}
	if has(f, "内置地理数据库超过 90 天") == nil {
		t.Errorf("地理库：%v", titles(f))
	}
	if f[len(f)-1].Severity != Info || f[0].Severity < f[len(f)-1].Severity {
		t.Error("结论应按严重程度排序")
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{512: "512 字节", 2048: "2 KB", 5 << 20: "5.0 MB", 43_000_000_000: "40.05 GB", 200_000_000_000: "186.26 GB"}
	for n, want := range cases {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
