package diag

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/settings"
)

func TestParsesMihomoConnectionLines(t *testing.T) {
	entry, ok := ParseRouteLogLine(`time="2026-10-13T19:05:01.123+08:00" level=info msg="[TCP] 198.18.0.1:60822 --> www.baidu.com:443 match DomainSuffix(baidu.com) using DIRECT"`)
	if !ok || entry.Host != "www.baidu.com" || entry.Port != 443 || entry.Rule != "DomainSuffix(baidu.com)" || entry.Route() != RouteDirect {
		t.Fatalf("got %+v", entry)
	}
	proxied, ok := ParseRouteLogLine("level=info msg=\"[TCP] 198.18.0.1:60823(chrome.exe) --> www.google.com:443 match GeoSite(geolocation-!cn) using PROXY[AUTO[香港-01]]\"\r")
	if !ok || proxied.Route() != RouteProxy || proxied.Chain != "PROXY[AUTO[香港-01]]" {
		t.Fatalf("got %+v", proxied)
	}
	if fallback, _ := ParseRouteLogLine(`level=info msg="[TCP] 198.18.0.1:1 --> unknown.example:443 match Match using PROXY[AUTO[x]]"`); fallback.Rule != "Match" {
		t.Fatalf("got %+v", fallback)
	}
	if _, ok := ParseRouteLogLine(`level=info msg="Start initial configuration"`); ok {
		t.Error("不是连接记录")
	}
}

func TestLatestEntryForHostIsTheLastOne(t *testing.T) {
	log := `level=info msg="[TCP] 198.18.0.1:1 --> a.com:443 match Match using PROXY[x]"
level=info msg="[TCP] 198.18.0.1:2 --> b.com:443 match Match using DIRECT"
level=info msg="[TCP] 198.18.0.1:3 --> a.com:443 match DomainSuffix(a.com) using DIRECT"`
	if e, ok := LatestRouteFor("a.com", log); !ok || e.Chain != "DIRECT" {
		t.Fatalf("got %+v", e)
	}
	if _, ok := LatestRouteFor("c.com", log); ok {
		t.Error("没有记录时应返回 false")
	}
}

func reportInput() Input {
	in := healthyInput()
	in.AppPhase = "已连接"
	in.Settings.Subscription.URL = "https://sub.example.com/api/v1/client/subscribe?token=9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	in.Status.ControllerSecret = "super-secret-controller-key"
	in.CoreLog = `time="t" level=warning msg="dial https://sub.example.com/api/v1/client/subscribe?token=9f8e7d6c5b4a39281706f5e4d3c2b1a0 failed"
time="t" level=info msg="[TCP] 198.18.0.1:1 --> secret-site.example:443 match Match using PROXY[x]"`
	in.HelperLog = "Authorization: Bearer super-secret-controller-key"
	home, _ := os.UserHomeDir()
	in.UserPaths = paths.UserAt(home)
	in.System = paths.SystemAt(`C:\ProgramData\MyTizi`)
	in.InstallDir = `C:\Program Files\MyTizi`
	in.Facts.OS = "Windows 11 Pro 24H2（26100.4061）"
	in.Facts.Arch = "amd64"
	return in
}

func TestReportIsSelfContainedAndNeverLeaksSecrets(t *testing.T) {
	in := reportInput()
	summary := Render(in, false)
	full := Render(in, true)
	home, _ := os.UserHomeDir()
	for _, report := range []string{summary, full} {
		if strings.Contains(report, "9f8e7d6c5b4a39281706f5e4d3c2b1a0") {
			t.Error("订阅 token 泄漏")
		}
		if strings.Contains(report, "super-secret-controller-key") {
			t.Error("控制密钥泄漏")
		}
		if home != "" && strings.Contains(report, home) {
			t.Error("家目录没有替换成 ~")
		}
		for _, want := range []string{
			"sub.example.com", "## 给 AI 的说明", "## 1. 自动识别的结论", "## 2. 当前状态", "## 6. 系统网络",
			"## 8. 文件位置", "## 9. 日志", "match X using Y", "strict-route", `\\.\pipe\MyTizi.Helper`,
			in.System.CoreLog(), `$env:ProgramData\MyTizi\logs\mihomo.log`, "```powershell", "sc.exe query MyTiziHelper",
			"Windows 11 Pro 24H2", "msftconnecttest",
		} {
			if !strings.Contains(report, want) {
				t.Errorf("报告缺少 %q", want)
			}
		}
		for _, mac := range []string{"networksetup", "LaunchDaemon", "/etc/resolver", "launchctl", "macOS"} {
			if strings.Contains(report, mac) {
				t.Errorf("Windows 报告里不应出现 %q", mac)
			}
		}
	}
	if strings.Contains(summary, "secret-site.example") {
		t.Error("摘要版不含连接日志里的域名")
	}
	if !strings.Contains(full, "secret-site.example") || !strings.Contains(full, "完整版") {
		t.Error("完整版应包含最近连接")
	}
}

func TestReportRendersWithEmptyInput(t *testing.T) {
	report := Render(Input{}, false)
	if !strings.Contains(report, "还没有添加订阅") || !strings.Contains(report, "尚未添加订阅") {
		t.Fatal(report)
	}
}

// Gather 通过真实的 ipc 客户端拿到状态，并从控制接口读线路延迟（控制接口用 httptest 模拟）。
func TestGatherUsesServiceAndController(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"version":"v1.19.32"}`))
		case "/providers/proxies/sub":
			_, _ = w.Write([]byte(`{"name":"sub","proxies":[{"name":"HK","type":"Shadowsocks","history":[{"delay":88}]},{"name":"JP","type":"Vmess","history":[]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	port := controller.Listener.Addr().(*net.TCPAddr).Port

	dir, err := os.MkdirTemp("", "mt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := dir + "/s"
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("当前平台不支持 unix socket：", err)
	}
	defer listener.Close()
	started := time.Now()
	go ipc.Serve(listener, func(req ipc.Request) ipc.Response {
		switch req.Method {
		case ipc.MethodHello:
			return ipc.Response{OK: true, Hello: &ipc.Hello{IPCVersion: 1, ServiceVersion: "dev", CoreInstalled: true, InstallDir: dir}}
		default:
			return ipc.Response{OK: true, Status: &ipc.Status{Phase: ipc.PhaseRunning, ControllerPort: port, ControllerSecret: "abcdef123456", StartedAt: &started, StrictRoute: true}}
		}
	}, func(string, ...any) {})
	client := &ipc.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}

	base := Input{AppVersion: "dev", Settings: settings.Default(), UserPaths: paths.UserAt(dir), System: paths.SystemAt(dir)}
	in := Gather(context.Background(), base, client, false)
	if in.Hello == nil || in.Status == nil || in.ServiceError != "" {
		t.Fatalf("应拿到服务状态：%+v %+v %s", in.Hello, in.Status, in.ServiceError)
	}
	if in.CoreVersion != "v1.19.32" || len(in.Nodes) != 2 || in.Nodes[0].Delay != 88 || in.Nodes[1].Delay != 0 {
		t.Fatalf("控制接口数据不对：%s %+v", in.CoreVersion, in.Nodes)
	}
	if in.InstallDir != dir {
		t.Errorf("安装目录应取自服务：%s", in.InstallDir)
	}
	report := Render(in, false)
	if strings.Contains(report, "abcdef123456") {
		t.Error("控制密钥泄漏")
	}

	listener.Close()
	in = Gather(context.Background(), base, client, false)
	if in.ServiceError == "" || in.Status != nil {
		t.Fatalf("服务不在时应记录错误：%+v", in)
	}
}

func TestProbeReadsRouteFromCoreLog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	log := `level=info msg="[TCP] 198.18.0.1:5 --> 127.0.0.1:443 match DomainSuffix(x) using PROXY[AUTO[HK]]"`
	result := runProbe(context.Background(), Probe{Name: "测试", URL: server.URL + "/generate_204", Expected: RouteDirect}, func() string { return log })
	if !result.Reachable || result.Status != 204 || result.ObservedRoute != RouteProxy || !result.RouteMismatch() || result.Passed() {
		t.Fatalf("got %+v", result)
	}
	dead := runProbe(context.Background(), Probe{Name: "不通", URL: "http://127.0.0.1:1/", Expected: RouteProxy}, func() string { return "" })
	if dead.Reachable || dead.Error == "" || dead.Passed() {
		t.Fatalf("got %+v", dead)
	}
}
