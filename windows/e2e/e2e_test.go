//go:build windows && e2e

// 端到端测试：在真实的 Windows 上（CI 用 GitHub Actions 的 Windows 虚拟机，管理员身份），
// 针对已经用安装包装好的 MyTizi 验证整条链路：
//
//	后台服务 → 生成配置 → mihomo 创建 Wintun 网卡 → DNS 被接管（fake-ip）→ 国内直连、海外走节点
//	→ 核心被杀后网络自动恢复 → 服务被杀后核心随之结束、服务自动重启 → 断开后一切复原。
//
// "节点"是本机另一个 mihomo（同一个可执行文件，出站绑在物理网卡上），以 HTTP 代理的形式提供给被测核心。
//
//	go test -tags e2e -count=1 -v -timeout 20m ./e2e/
package e2e

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/mihomo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
)

func installDir() string {
	if dir := os.Getenv("MYTIZI_INSTALL_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(os.Getenv("ProgramFiles"), "MyTizi")
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("等待超时（%v）：%s", timeout, what)
}

// physicalInterface 返回默认路由所在网卡的名字（连接之前调用）。
func physicalInterface(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`$r = Get-NetRoute -DestinationPrefix 0.0.0.0/0 | Sort-Object RouteMetric | Select-Object -First 1; (Get-NetAdapter -InterfaceIndex $r.InterfaceIndex).Name`).Output()
	if err != nil {
		t.Fatalf("找不到默认路由网卡：%v", err)
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		t.Fatal("找不到默认路由网卡")
	}
	return name
}

// startNode 启动"节点"：本机 127.0.0.1:17890 上的 HTTP 代理，出站与 DNS 都绑在物理网卡上，不经过被测的 TUN。
func startNode(t *testing.T, iface string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "node.log")
	config := fmt.Sprintf(`mixed-port: 17890
bind-address: "127.0.0.1"
allow-lan: false
mode: rule
log-level: info
ipv6: false
geo-auto-update: false
interface-name: %q
dns:
  enable: true
  ipv6: false
  default-nameserver: ["8.8.8.8#%s"]
  nameserver: ["8.8.8.8#%s", "1.1.1.1#%s"]
rules:
  - "MATCH,DIRECT"
`, iface, iface, iface, iface)
	if err := os.WriteFile(filepath.Join(dir, "node.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(paths.ComponentsIn(installDir()).Core(), "-d", dir, "-f", filepath.Join(dir, "node.yaml"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); logFile.Close() })
	waitUntil(t, "节点监听 17890", 15*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:17890", time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	return logPath
}

const subscriptionYAML = `proxies:
  - {name: "e2e-node", type: http, server: 127.0.0.1, port: 17890}
`

func lookup(host string) (net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	return ips[0], nil
}

func isFakeIP(ip net.IP) bool { v4 := ip.To4(); return v4 != nil && v4[0] == 198 && v4[1] == 18 }

func flushDNS() { _ = exec.Command("ipconfig", "/flushdns").Run() }

func get(t *testing.T, url string) int {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return resp.StatusCode
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	t.Fatalf("GET %s 失败：%v", url, lastErr)
	return 0
}

// rawDNSQuery 直接向某个 DNS 服务器发 UDP 查询，返回第一个 A 记录。用来确认 53 端口被 TUN 劫持。
func rawDNSQuery(server, name string) (net.IP, error) {
	conn, err := net.DialTimeout("udp", server, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	query := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(name, ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	msg := buf[:n]
	answers := binary.BigEndian.Uint16(msg[6:8])
	offset := len(query)
	for i := 0; i < int(answers) && offset+12 <= len(msg); i++ {
		if msg[offset]&0xC0 == 0xC0 {
			offset += 2
		} else {
			for offset < len(msg) && msg[offset] != 0 {
				offset += int(msg[offset]) + 1
			}
			offset++
		}
		kind := binary.BigEndian.Uint16(msg[offset : offset+2])
		length := int(binary.BigEndian.Uint16(msg[offset+8 : offset+10]))
		offset += 10
		if kind == 1 && length == 4 && offset+4 <= len(msg) {
			return net.IP(msg[offset : offset+4]), nil
		}
		offset += length
	}
	return nil, errors.New("响应里没有 A 记录")
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

func tunPresent() bool {
	iface, err := net.InterfaceByName(appinfo.TunDevice)
	if err != nil {
		return false
	}
	addrs, _ := iface.Addrs()
	for _, a := range addrs {
		if strings.HasPrefix(a.String(), appinfo.TunGateway+"/") {
			return true
		}
	}
	return false
}

func dacl(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("读取 %s 的权限失败：%v", path, err)
	}
	return sd.String()
}

func TestEndToEnd(t *testing.T) {
	ctx := context.Background()
	client := ipc.NewClient()
	sys := paths.DefaultSystem()

	// 1. 安装结果：服务在运行、协议版本一致、系统目录权限正确。
	waitUntil(t, "后台服务运行", 30*time.Second, func() bool { return svcctl.Query() == svcctl.Running })
	hello, err := client.Hello(ctx)
	if err != nil {
		t.Fatalf("hello 失败：%v", err)
	}
	if err := ipc.CheckHello(hello); err != nil || !hello.CoreInstalled {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	if want := os.Getenv("MYTIZI_EXPECT_VERSION"); want != "" && hello.ServiceVersion != want {
		t.Fatalf("服务版本 %s，期望 %s", hello.ServiceVersion, want)
	}
	t.Logf("服务 %s，pid %d，安装目录 %s", hello.ServiceVersion, hello.ServicePID, hello.InstallDir)
	if d := dacl(t, sys.CoreHome); !strings.HasPrefix(d, "D:P") || strings.Contains(d, ";;;BU)") {
		t.Errorf("core-home 的权限不应允许普通用户：%s", d)
	}
	if d := dacl(t, sys.LogDir); !strings.Contains(d, ";;;BU)") {
		t.Errorf("日志目录应允许普通用户读取：%s", d)
	}

	iface := physicalInterface(t)
	t.Logf("物理网卡：%s", iface)
	nodeLog := startNode(t, iface)

	connect := func() *ipc.Status {
		t.Helper()
		st, err := client.Start(ctx, ipc.StartParams{
			Subscription: []byte(subscriptionYAML), StrictRoute: true,
			CustomRules: []string{"DOMAIN-SUFFIX,example.org,PROXY"},
		})
		if err != nil {
			t.Fatalf("连接失败：%v\n%s", err, rotlog.Tail(sys.CoreLog(), 40, 0))
		}
		if st.Phase != ipc.PhaseRunning || st.TunInterface != appinfo.TunDevice || st.CorePID == 0 {
			t.Fatalf("状态不对：%+v", st)
		}
		return st
	}
	disconnect := func() {
		t.Helper()
		if _, err := client.Stop(ctx); err != nil {
			t.Fatalf("断开失败：%v", err)
		}
	}
	t.Cleanup(func() { _, _ = client.Stop(context.Background()) })

	// 2. 连接：TUN 网卡出现，系统解析拿到 fake-ip，直连 53 端口也被劫持。
	st := connect()
	t.Logf("已连接：核心 pid %d，控制端口 %d", st.CorePID, st.ControllerPort)
	if !tunPresent() {
		t.Fatal("没有找到 198.18.0.1 的 MyTizi 网卡")
	}
	flushDNS()
	ip, err := lookup("www.baidu.com")
	if err != nil || !isFakeIP(ip) {
		t.Fatalf("系统 DNS 应返回 fake-ip，得到 %v %v", ip, err)
	}
	if ip, err := rawDNSQuery("8.8.8.8:53", "www.gstatic.com"); err != nil || !isFakeIP(ip) {
		t.Errorf("发往 8.8.8.8:53 的查询应被劫持成 fake-ip，得到 %v %v", ip, err)
	}

	// 3. 分流：国内直连、海外经节点；核心日志里能看到命中的规则。
	if code := get(t, "https://www.baidu.com/"); code != 200 {
		t.Errorf("直连 baidu 返回 %d", code)
	}
	if code := get(t, "https://www.gstatic.com/generate_204"); code != 204 {
		t.Errorf("经代理访问 gstatic 返回 %d", code)
	}
	if code := get(t, "https://example.org/"); code != 200 {
		t.Errorf("自定义规则走代理的 example.org 返回 %d", code)
	}
	time.Sleep(800 * time.Millisecond)
	coreLog := rotlog.Tail(sys.CoreLog(), 400, 0)
	for host, want := range map[string]string{"www.baidu.com": "DIRECT", "www.gstatic.com": "e2e-node", "example.org": "e2e-node"} {
		line := ""
		for _, l := range strings.Split(coreLog, "\n") {
			if strings.Contains(l, "--> "+host+":443") {
				line = l
			}
		}
		if !strings.Contains(line, want) {
			t.Errorf("%s 应命中 %s，核心日志：%q", host, want, line)
		}
	}
	if node, _ := os.ReadFile(nodeLog); !strings.Contains(string(node), "www.gstatic.com") {
		t.Errorf("节点日志里没有 gstatic 的连接：\n%s", node)
	}
	api := mihomo.New(st.ControllerPort, st.ControllerSecret)
	if provider, err := api.Provider(ctx, "sub"); err != nil || len(provider.Proxies) != 1 {
		t.Errorf("控制接口里的线路不对：%+v %v", provider, err)
	}
	if delays, err := api.GroupDelay(ctx, "AUTO", "https://www.gstatic.com/generate_204", 5000); err != nil || delays["e2e-node"] == 0 {
		t.Errorf("节点测速失败：%v %v", delays, err)
	}

	// 4. 诊断命令行：能跑通，并带上自检结果。
	out, err := exec.Command(filepath.Join(installDir(), "MyTizi.exe"), "--diagnose").Output()
	report := string(out)
	if err != nil || !strings.Contains(report, "MyTizi 诊断报告") || !strings.Contains(report, "实机自检") {
		t.Errorf("诊断报告不完整：%v\n%s", err, report)
	}
	if !strings.Contains(report, "✅") {
		t.Errorf("自检应至少有一项通过：\n%s", report)
	}
	if os.Getenv("GITHUB_STEP_SUMMARY") != "" {
		_ = os.WriteFile(filepath.Join(os.TempDir(), "mytizi-diagnose.md"), out, 0o644)
	}

	// 5. 核心被杀：服务报告崩溃，网卡消失，解析恢复真实地址，直连可用。
	_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(st.CorePID)).Run()
	waitUntil(t, "服务报告核心崩溃", 10*time.Second, func() bool {
		s, err := client.Status(ctx)
		return err == nil && s.Phase == ipc.PhaseCrashed
	})
	waitUntil(t, "虚拟网卡消失", 15*time.Second, func() bool { return !tunPresent() })
	waitUntil(t, "解析恢复为真实地址", 15*time.Second, func() bool {
		flushDNS()
		ip, err := lookup("www.baidu.com")
		return err == nil && !isFakeIP(ip)
	})
	if code := get(t, "https://www.baidu.com/"); code != 200 {
		t.Errorf("核心崩溃后直连应恢复，得到 %d", code)
	}

	// 6. 服务被强杀：作业对象让核心一起结束；服务管理器在几秒后把服务拉起来。
	st = connect()
	corePID := st.CorePID
	hello, _ = client.Hello(ctx)
	_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(hello.ServicePID)).Run()
	waitUntil(t, "核心随服务结束", 10*time.Second, func() bool { return !processAlive(corePID) })
	waitUntil(t, "虚拟网卡消失", 15*time.Second, func() bool { return !tunPresent() })
	waitUntil(t, "服务自动重启", 60*time.Second, func() bool {
		h, err := client.Hello(ctx)
		return err == nil && h.ServicePID != hello.ServicePID
	})
	if s, err := client.Status(ctx); err != nil || s.Phase != ipc.PhaseStopped {
		t.Errorf("重启后的服务应处于未连接：%+v %v", s, err)
	}

	// 7. 正常连接再断开：网卡消失，解析恢复。
	connect()
	disconnect()
	waitUntil(t, "断开后网卡消失", 15*time.Second, func() bool { return !tunPresent() })
	flushDNS()
	if ip, err := lookup("www.baidu.com"); err != nil || isFakeIP(ip) {
		t.Errorf("断开后应解析到真实地址，得到 %v %v", ip, err)
	}
	if code := get(t, "https://www.baidu.com/"); code != 200 {
		t.Errorf("断开后直连返回 %d", code)
	}
}
