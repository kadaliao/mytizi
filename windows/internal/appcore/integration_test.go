package appcore

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/diag"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/probe"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/service"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/subscription"
	"github.com/kadaliao/mytizi/windows/internal/testutil"
)

// 把除 TUN 以外的一切都接成真的：后台服务（真实 mihomo、真实进程管理）跑在本地 socket 上，
// App 状态机用真实的 IPC 客户端、真实的订阅下载与解析，诊断报告也是真实生成的。
func TestIntegrationAddConnectRefreshDisconnect(t *testing.T) {
	core := testutil.RequireCore(t)

	// 安装目录：核心与地理库
	install := t.TempDir()
	components := paths.ComponentsIn(install)
	_ = os.MkdirAll(components.Dir, 0o755)
	for src, dst := range map[string]string{
		core: components.Core(), filepath.Join(testutil.VendorDir(), "geoip.metadb"): components.GeoIP(),
		filepath.Join(testutil.VendorDir(), "geosite.dat"): components.GeoSite(),
	} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sys := paths.SystemAt(t.TempDir())
	log, coreLog := rotlog.New(sys.HelperLog(), 0, 0), rotlog.New(sys.CoreLog(), 0, 0)
	t.Cleanup(func() { log.Close(); coreLog.Close() })
	supervisor := service.New(service.Options{
		System: sys, Components: components, TUN: false, Platform: service.DefaultPlatform{},
		Log: log, CoreLog: coreLog,
	})
	t.Cleanup(supervisor.Shutdown)
	socketDir, _ := os.MkdirTemp("", "mti")
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("当前平台不支持 unix socket：", err)
	}
	t.Cleanup(func() { listener.Close() })
	go ipc.Serve(listener, supervisor.Handle, func(string, ...any) {})
	client := &ipc.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}

	// 订阅服务器
	body := testutil.SampleClashSubscription
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("subscription-userinfo", "upload=1073741824; download=42949672960; total=107374182400; expire=1800000000")
		w.Header().Set("profile-title", "base64:5YWs5Y+4")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	user := paths.UserAt(t.TempDir())
	var clipboard string
	c := New(Deps{
		Client: client, Store: settings.NewStore(user), Paths: user, Version: "dev",
		Probe: func(ctx context.Context, b []byte) ([]settings.Node, error) {
			return probe.Nodes(ctx, b, core, user.Cache, 15*time.Second)
		},
		Fetch: func(ctx context.Context, link string) (*subscription.Fetched, error) {
			u, err := subscription.NormalizeURL(link)
			if err != nil {
				return nil, err
			}
			return subscription.Fetch(ctx, u, 10*time.Second)
		},
		Diagnose: func(ctx context.Context, full bool, info DiagContext) string {
			var nodes []diag.NodeDelay
			for _, n := range info.Nodes {
				nodes = append(nodes, diag.NodeDelay{Name: n.Name, Type: n.Type, Delay: n.Delay})
			}
			return diag.Report(ctx, diag.Input{
				AppVersion: "dev", AppPhase: info.AppPhase, Settings: info.Settings, Nodes: nodes, UserPaths: user,
			}, client, full, false)
		},
		SetClipboard: func(s string) error { clipboard = s; return nil },
	})
	t.Cleanup(c.Stop)

	link := server.URL + "/api/v1/client/subscribe?token=integration-secret-token"
	c.AddSubscription(link)
	s := c.State()
	if s.AddError != "" || s.Phase != PhaseDisconnected || s.Subscription == nil || s.Subscription.Name != "公司" ||
		s.Subscription.NodeCount != 2 || s.Subscription.Usage == nil || s.Subscription.Usage.UsedText != "41.00 GB" {
		t.Fatalf("添加订阅后的状态不对：%+v / %+v", s, s.Subscription)
	}

	c.Connect()
	s = c.State()
	if s.Phase != PhaseConnected || s.ErrorMessage != "" {
		t.Fatalf("连接失败：%+v\n%s", s, rotlog.Tail(sys.CoreLog(), 30, 0))
	}
	if s.Selection != AutoSelection || len(s.Nodes) != 2 {
		t.Errorf("应读出线路：%+v", s)
	}
	config, _ := os.ReadFile(sys.ConfigPath())
	if !strings.Contains(string(config), "DOMAIN,127.0.0.1,DIRECT") {
		t.Errorf("订阅主机应固定直连：\n%s", config)
	}
	redacted, _ := os.ReadFile(user.LastConfigRedacted())
	if len(redacted) == 0 || strings.Contains(string(redacted), "integration-secret-token") {
		t.Error("用户目录里应有一份不含密钥的配置副本")
	}

	// 已连接时刷新订阅：新内容推给服务，核心重载 provider。
	body = "proxies:\n  - {name: \"新加坡-09\", type: http, server: 192.0.2.9, port: 8080}\n"
	c.RefreshSubscription()
	s = c.State()
	if s.Subscription.NodeCount != 1 || s.Nodes[0].Name != "新加坡-09" {
		t.Fatalf("刷新后的线路不对：%+v", s.Nodes)
	}
	written, _ := os.ReadFile(filepath.Join(sys.CoreHome, "providers", "sub.yaml"))
	if string(written) != body {
		t.Error("刷新后的订阅没有写进核心目录")
	}

	// 切换线路
	c.SelectNode("新加坡-09")
	if s := c.State(); s.Selection != "新加坡-09" {
		t.Errorf("切换线路失败：%+v", s)
	}

	// 诊断报告：真实生成、已脱敏
	c.CopyDiagnostics(false)
	for _, want := range []string{"MyTizi 诊断报告", "已连接", "127.0.0.1"} {
		if !strings.Contains(clipboard, want) {
			t.Errorf("诊断报告里缺少 %q", want)
		}
	}
	if strings.Contains(clipboard, "integration-secret-token") {
		t.Fatal("诊断报告泄漏了订阅 token")
	}

	c.Disconnect()
	if s := c.State(); s.Phase != PhaseDisconnected {
		t.Fatalf("断开失败：%+v", s)
	}
	if st, _ := client.Status(context.Background()); st.Phase != ipc.PhaseStopped {
		t.Errorf("服务应已停止核心：%+v", st)
	}
}
