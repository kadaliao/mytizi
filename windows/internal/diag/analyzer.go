package diag

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/rules"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
)

// NodeDelay 是一条线路的最近测速结果。Delay 为 0 表示不可用/未测。
type NodeDelay struct {
	Name  string
	Type  string
	Delay int
}

// Input 是诊断的全部输入。App 填它自己知道的部分（版本、阶段、设置、最近错误），Gather 补齐其余。
type Input struct {
	AppVersion       string
	AppPhase         string
	LastError        string
	Settings         settings.Settings
	Nodes            []NodeDelay
	CustomRuleCount  int
	CustomRulesError string
	UserPaths        paths.User

	System     paths.System
	InstallDir string
	// GeoDate 是安装目录里地理库文件的时间；零值表示不知道。
	GeoDate time.Time

	ServiceState svcctl.State
	Hello        *ipc.Hello
	Status       *ipc.Status
	ServiceError string
	CoreVersion  string

	Facts    Facts
	SelfTest []SelfTestResult

	AppLog    string
	HelperLog string
	CoreLog   string
	Now       time.Time
}

func (in Input) IsRunning() bool { return in.Status != nil && in.Status.Phase == ipc.PhaseRunning }

// strictRoute 是实际生效的 strict-route：连接中以服务报告的为准，否则看设置。
func (in Input) strictRoute() bool {
	if in.IsRunning() {
		return in.Status.StrictRoute
	}
	return in.Settings.StrictRoute
}

// Severity 是结论的严重程度。
type Severity int

const (
	Info Severity = iota
	Warning
	Problem
)

type Finding struct {
	Severity Severity
	Title    string
	Detail   string
}

func (f Finding) Marker() string {
	switch f.Severity {
	case Problem:
		return "🔴 问题"
	case Warning:
		return "🟡 注意"
	default:
		return "ℹ️ 提示"
	}
}

// Findings 把原始事实归纳成"哪里出了问题"。纯函数：给定输入就有确定的结论。
// 这是诊断里价值最高的部分——AI（和用户）先看结论，再看细节。
func Findings(in Input) []Finding {
	var out []Finding
	out = append(out, serviceFindings(in)...)
	out = append(out, subscriptionFindings(in)...)
	out = append(out, coreStateFindings(in)...)
	if in.IsRunning() {
		out = append(out, runningFindings(in)...)
	} else {
		out = append(out, notRunningFindings(in)...)
	}
	out = append(out, proxyFindings(in)...)
	out = append(out, conflictFindings(in)...)
	out = append(out, selfTestFindings(in)...)
	out = append(out, logSignatureFindings(in)...)
	out = append(out, geoFindings(in)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity > out[j].Severity })
	return out
}

// MARK: 1. 后台服务

func serviceFindings(in Input) []Finding {
	logHint := fmt.Sprintf("服务日志 %s；也可以执行 `sc.exe query %s` 查看状态。", in.system().HelperLog(), appinfo.ServiceName)
	switch {
	case in.ServiceState == svcctl.NotInstalled:
		return []Finding{{Problem, "后台组件没有安装",
			fmt.Sprintf("服务 %s 不存在。重新运行安装程序，或在 App 里点「修复后台组件」（会请求一次管理员授权）。", appinfo.ServiceName)}}
	case in.ServiceError != "" && (in.ServiceState == svcctl.Stopped || in.ServiceState == svcctl.Stopping):
		return []Finding{{Problem, "后台组件没有运行",
			fmt.Sprintf("服务 %s 处于「%s」状态。在 App 里点「修复后台组件」，或执行 `sc.exe start %s`。%s",
				appinfo.ServiceName, in.ServiceState.Describe(), appinfo.ServiceName, logHint)}}
	case in.ServiceError != "":
		message := strings.TrimRight(in.ServiceError, "。")
		return []Finding{{Problem, "无法连接后台组件",
			fmt.Sprintf("%s。服务状态：%s。如果服务在运行却连不上，可能是别的程序占用了命名管道 %s。%s",
				message, in.ServiceState.Describe(), appinfo.PipeName, logHint)}}
	}
	var out []Finding
	if in.Hello != nil {
		if err := ipc.CheckHello(in.Hello); err != nil {
			out = append(out, Finding{Problem, "后台组件版本不匹配", err.Error() + "。"})
		}
		if !in.Hello.CoreInstalled {
			out = append(out, Finding{Problem, "内置核心缺失",
				fmt.Sprintf("安装目录 %s 里没有 core\\mihomo.exe。请重新运行安装程序（杀毒软件可能把它隔离了）。", in.Hello.InstallDir)})
		}
		if in.Hello.ServiceVersion != "" && in.AppVersion != "" && in.Hello.ServiceVersion != in.AppVersion {
			out = append(out, Finding{Warning, "App 与后台组件的版本不同",
				fmt.Sprintf("App %s，后台组件 %s。重新运行安装程序即可统一。", in.AppVersion, in.Hello.ServiceVersion)})
		}
	}
	return out
}

func (in Input) system() paths.System {
	if in.System.Base == "" {
		return paths.DefaultSystem()
	}
	return in.System
}

// MARK: 2. 订阅

func subscriptionFindings(in Input) []Finding {
	var out []Finding
	sub := in.Settings.Subscription
	if sub == nil {
		out = append(out, Finding{Problem, "还没有添加订阅", "settings.json 里没有订阅链接。"})
	} else {
		if len(sub.Nodes) == 0 {
			out = append(out, Finding{Problem, "订阅里没有线路", fmt.Sprintf("订阅 %s 解析出 0 个节点。", sub.Host())})
		}
		if sub.UpdatedAt != nil && in.Now.Sub(*sub.UpdatedAt) > 7*24*time.Hour {
			out = append(out, Finding{Warning, "订阅超过 7 天没有更新",
				fmt.Sprintf("上次更新 %s。节点信息可能已经过期。", FormatTime(*sub.UpdatedAt))})
		}
		if u := sub.Usage; u != nil {
			if u.Expire != nil && u.Expire.Before(in.Now) {
				out = append(out, Finding{Problem, "订阅已过期", fmt.Sprintf("到期时间 %s。", FormatTime(*u.Expire))})
			}
			if u.Total > 0 && u.Used() >= u.Total {
				out = append(out, Finding{Problem, "订阅流量已用完",
					fmt.Sprintf("已用 %s / 总量 %s。", FormatBytes(u.Used()), FormatBytes(u.Total))})
			}
		}
	}
	if in.CustomRulesError != "" {
		out = append(out, Finding{Warning, "自定义规则有错误（已被忽略）", in.CustomRulesError})
	}
	return out
}

// MARK: 3. 核心状态

func coreStateFindings(in Input) []Finding {
	if in.Status == nil || in.Status.Phase != ipc.PhaseCrashed {
		return nil
	}
	code, at := -1, "?"
	if e := in.Status.LastExit; e != nil {
		code, at = e.Code, FormatTime(e.At)
	}
	return []Finding{{Problem, "核心意外退出",
		fmt.Sprintf("退出码 %d，时间 %s。虚拟网卡与拦截规则已随核心一起消失，网络已恢复。详见核心日志末尾。", code, at)}}
}

// MARK: 4. 连接中

func runningFindings(in Input) []Finding {
	var out []Finding
	facts := in.Facts
	ours := facts.OurTUN()
	if ours == nil || !contains(ours.IPv4, appinfo.TunGateway) {
		out = append(out, Finding{Problem, "找不到 TUN 网卡",
			fmt.Sprintf("后台组件报告运行中，但系统里没有地址为 %s 的网卡（应为名为 %s 的 Wintun 网卡）。", appinfo.TunGateway, appinfo.TunDevice)})
	}
	if rivals := facts.RivalTUNs(); len(rivals) > 0 {
		var list []string
		for _, a := range rivals {
			list = append(list, fmt.Sprintf("%s %s", a.Name, strings.Join(a.IPv4, ",")))
		}
		out = append(out, Finding{Problem, "检测到另一个 TUN 在使用同一地址段",
			strings.Join(list, "；") + "。两个代理同时开 TUN 会互相抢路由和 DNS，请关闭另一个（常见是 Clash Verge 的 TUN 模式）。"})
	}
	out = append(out, egressFindings(in)...)
	out = append(out, dnsFindings(in)...)
	out = append(out, strictRouteFindings(in)...)
	out = append(out, nodeFindings(in)...)
	return out
}

// egressFindings：别的 VPN 的网卡排在默认路由第一位（典型：Tailscale 开了 exit node）。
// 本 App 的 TUN 路由更具体，应用流量仍会进本 App；要看的是核心自己的出站会不会被它接走。
// mihomo 在 Windows 上自动选出口时跳过 Wintun 类型的网卡，所以 Wintun 类 VPN 只是提醒；
// TAP 之类伪装成以太网的 VPN 网卡不会被跳过，核心连节点的流量可能整个经它转发。
func egressFindings(in Input) []Finding {
	var first *DefaultRoute
	for i := range in.Facts.DefaultRoutes {
		r := &in.Facts.DefaultRoutes[i]
		if r.InterfaceIP == appinfo.TunGateway || (r.Adapter != "" && r.Adapter == in.Status.TunInterface) {
			continue
		}
		first = r
		break
	}
	if first == nil {
		return nil
	}
	adapter := in.Facts.Adapter(first.Adapter)
	if adapter == nil || adapter.IsOurTUN() || !adapter.IsVirtualTunnel() {
		return nil
	}
	who := fmt.Sprintf("另一个 VPN（%s）", adapter.Name)
	advice := "建议关闭该 VPN 的全局模式。"
	if adapter.IsTailscale() {
		who = "Tailscale 的出口节点（exit node）"
		advice = "建议在 Tailscale 菜单里关闭 exit node，两者各管各的流量。"
	}
	where := fmt.Sprintf("默认路由第一位是 %s（%s，%s）。", adapter.Name, adapter.Description, strings.Join(adapter.IPv4, ", "))
	if adapter.IsWintunLike() {
		return []Finding{{Warning, who + "正在接管默认路由",
			where + "mihomo 选择出口时会跳过这类虚拟网卡，代理流量不会被它接走；但没有进本 App 的流量会经它转发。" + advice}}
	}
	return []Finding{{Problem, who + "正在接管默认路由",
		where + "这类 VPN 网卡在系统看来和物理网卡一样，mihomo 的出站（包括连节点）可能全部经它转发。" + advice}}
}

func dnsFindings(in Input) []Finding {
	ours := in.Facts.OurTUN()
	if ours == nil || contains(ours.DNS, appinfo.TunDNS) {
		return nil
	}
	current := "空"
	if len(ours.DNS) > 0 {
		current = strings.Join(ours.DNS, ", ")
	}
	return []Finding{{Problem, "TUN 网卡的 DNS 没有指向 " + appinfo.TunDNS,
		fmt.Sprintf("网卡 %s 的 DNS 是 %s。DNS 查询不经过 mihomo，fake-ip 和域名规则会失效。重新连接一次；仍然如此请附上 `Get-DnsClientServerAddress -AddressFamily IPv4` 的输出。", ours.Name, current)}}
}

func strictRouteFindings(in Input) []Finding {
	var out []Finding
	if !in.strictRoute() {
		detail := "Windows 会同时向所有网卡的 DNS 发查询，部分解析会绕过 mihomo 拿到真实地址，导致域名规则时灵时不灵。"
		if v6 := in.Facts.GlobalIPv6Adapters(); len(v6) > 0 {
			detail += fmt.Sprintf("另外网卡 %s 有公网 IPv6 地址，IPv6 流量不经过 TUN，会直接绕过规则。", strings.Join(v6, ", "))
		}
		out = append(out, Finding{Warning, "没有开启「严格接管」（strict-route）", detail + "建议在高级设置里打开后重新连接。"})
		return out
	}
	var namespaces []string
	tailscale := false
	for _, rule := range in.Facts.NRPT {
		namespaces = append(namespaces, strings.Join(rule.Namespace, ","))
		for _, server := range rule.NameServers {
			if server == rules.TailscaleDNS {
				tailscale = true
			}
		}
		for _, ns := range rule.Namespace {
			if strings.Contains(ns, "ts.net") {
				tailscale = true
			}
		}
	}
	if len(namespaces) > 0 {
		detail := fmt.Sprintf("这些域名的解析由系统交给各自指定的 DNS（%s），而「严格接管」会拦掉发往其他网卡的 DNS，它们可能解析失败。",
			strings.Join(namespaces, "；"))
		if tailscale {
			detail += "其中 Tailscale 的 MagicDNS 名字（*.ts.net）由本 App 交给 100.100.100.100 解析，仍然可用；短名字依赖 DNS 后缀，也会落到 ts.net 上。"
		} else {
			detail += "如果这是公司内网（DirectAccess 等）需要的域名，可以在自定义规则里把它们设为 DIRECT，或临时关闭「严格接管」。"
		}
		out = append(out, Finding{Warning, "系统里有按域名指定 DNS 的策略（NRPT）", detail})
	}
	return out
}

// nodeFindings：线路全挂时把"卡在哪一步"说清楚——连不上节点服务器、端口没有服务，还是握手/认证失败。
func nodeFindings(in Input) []Finding {
	if len(in.Nodes) == 0 {
		return nil
	}
	for _, n := range in.Nodes {
		if n.Delay > 0 {
			return nil
		}
	}
	lines := strings.Split(in.CoreLog, "\n")
	var proxyTests int
	proxyAllFailed := true
	directWorks := false
	for _, t := range in.SelfTest {
		if t.Expected == RouteProxy {
			proxyTests++
			if t.Reachable {
				proxyAllFailed = false
			}
		}
		if t.Expected == RouteDirect && t.Passed() {
			directWorks = true
		}
	}
	proxyAllFailed = proxyAllFailed && proxyTests > 0
	count := func(match func(string) bool) int {
		n := 0
		for _, l := range lines {
			if strings.Contains(l, "dial PROXY") && match(l) {
				n++
			}
		}
		return n
	}
	timeouts := count(func(l string) bool {
		return strings.Contains(l, "context deadline exceeded") || strings.Contains(l, "i/o timeout")
	})
	refused := count(func(l string) bool {
		return strings.Contains(l, "connection refused") || strings.Contains(l, "actively refused")
	})
	handshake := count(func(l string) bool {
		return strings.Contains(l, "tls:") || strings.Contains(l, "EOF") || strings.Contains(l, "authentication")
	})

	if directWorks && proxyAllFailed && timeouts >= 5 {
		return []Finding{{Problem, "节点服务器连不上（直连正常）",
			"直连自检全部正常，但所有走代理的连接都卡在“连接节点服务器”这一步" +
				fmt.Sprintf("（核心日志里 `dial PROXY … context deadline exceeded` %d 次），代理握手根本没有开始。", timeouts) +
				"这不是规则或 DNS 问题。按顺序检查：1) 节点服务器（自建 FRP/frps、VPS）是否在线、端口映射是否还在；" +
				"2) 节点域名是否还解析到当前公网 IP（DDNS 过期会一直超时）；" +
				"3) 节点是否只暴露了 IPv6（连接期间 IPv6 出站被拦截）；" +
				"4) 换一个网络（手机热点）再试，排除运营商拦截端口。"}}
	}
	if directWorks && proxyAllFailed && refused >= 3 {
		return []Finding{{Problem, "节点端口没有服务（直连正常）",
			fmt.Sprintf("直连正常，但连节点服务器时被拒绝（`connection refused` %d 次）：", refused) +
				"地址能到，但那个端口没有程序在监听。检查节点端口是否正确、服务端是否启动、FRP 映射是否还在。"}}
	}
	if directWorks && proxyAllFailed && handshake >= 3 {
		return []Finding{{Problem, "节点可达但握手/认证失败（直连正常）",
			fmt.Sprintf("直连正常、节点服务器也能连上，但代理协议握手失败（TLS/EOF/认证错误 %d 次）。", handshake) +
				"检查节点类型、密码/UUID、SNI 与端口是否和订阅一致。"}}
	}
	return []Finding{{Problem, "所有线路测速都失败",
		fmt.Sprintf("%d 个线路都没有可用的延迟记录。可能是节点全部不可用，或者这个网络环境挡住了代理协议。", len(in.Nodes))}}
}

// MARK: 5. 未连接

func notRunningFindings(in Input) []Finding {
	if in.Status == nil || (in.Status.Phase != ipc.PhaseStopped && in.Status.Phase != ipc.PhaseCrashed) {
		return nil
	}
	ours := in.Facts.OurTUN()
	if ours == nil || !ours.Up {
		return nil
	}
	return []Finding{{Problem, "没有在连接，但 MyTizi 虚拟网卡还在",
		fmt.Sprintf("网卡 %s（%s）仍然在线，系统可能还在把流量和 DNS 交给它，表现为断开后上不了网。"+
			"以管理员身份执行 `sc.exe stop %s` 再 `sc.exe start %s`（会结束残留的核心），然后 `ipconfig /flushdns`。",
			ours.Name, strings.Join(ours.IPv4, ","), appinfo.ServiceName, appinfo.ServiceName)}}
}

// MARK: 6. 系统代理与冲突

func proxyFindings(in Input) []Finding {
	p := in.Facts.Proxy
	if len(p.DeadEndpoints) > 0 {
		return []Finding{{Problem, "系统代理指向一个没有程序在监听的端口",
			fmt.Sprintf("Internet 选项里的代理是 %s，其中 %s 没有程序在监听，浏览器等会全部打不开。"+
				"通常是别的代理软件退出时没有还原。在「设置 → 网络和 Internet → 代理」里关闭「使用代理服务器」。",
				p.Server, strings.Join(p.DeadEndpoints, ", "))}}
	}
	if !in.IsRunning() || !p.Active() {
		return nil
	}
	parts := []string{}
	if p.Enabled && p.Server != "" {
		parts = append(parts, "代理服务器 "+p.Server)
	}
	if p.AutoConfigURL != "" {
		parts = append(parts, "自动配置脚本 "+p.AutoConfigURL)
	}
	return []Finding{{Warning, "系统代理仍然开启",
		strings.Join(parts, "，") + "。使用 TUN 时不需要系统代理，浏览器的流量会先交给它；如果那个代理不可用，浏览器会打不开而别的程序正常。"}}
}

func conflictFindings(in Input) []Finding {
	if len(in.Facts.OtherProxyProcesses) == 0 {
		return nil
	}
	list := in.Facts.OtherProxyProcesses
	if len(list) > 6 {
		list = list[:6]
	}
	return []Finding{{Warning, "检测到其他代理/VPN 进程",
		strings.Join(list, "；") + "。它们如果也开了 TUN 或改了系统代理，会与本 App 冲突。"}}
}

// MARK: 7. 自检

func selfTestFindings(in Input) []Finding {
	var out []Finding
	for _, t := range in.SelfTest {
		if t.Passed() {
			continue
		}
		expected := string(t.Expected)
		if expected == "" {
			expected = "?"
		}
		observed := t.Observed
		if observed == "" {
			observed = "日志中没有记录"
		}
		switch {
		case t.Concern != "" && in.IsRunning():
			out = append(out, Finding{Problem, "自检失败：" + t.Name, t.Concern})
		case !t.Reachable:
			errText := t.Error
			if errText == "" {
				errText = "无响应"
			}
			out = append(out, Finding{Problem, "自检失败：" + t.Name + " 不通",
				fmt.Sprintf("%s：%s，用时 %d ms，期望%s，实际%s。", t.URL, errText, t.Milliseconds, expected, observed)})
		case t.RouteMismatch():
			out = append(out, Finding{Warning, "自检：" + t.Name + " 的走向和预期不同",
				fmt.Sprintf("期望%s，实际 %s。如果是国内平台，应补充直连域名；如果是海外站点，说明命中了直连规则。", expected, observed)})
		}
	}
	return out
}

// MARK: 8. 核心日志里的高频错误

var logSignatures = []struct{ needle, meaning string }{
	{"i/o timeout", "连接超时：节点不可达或被网络阻断"},
	{"no such host", "域名解析失败：国内 DNS 不可用或域名不存在"},
	{"connection refused", "连接被拒绝：节点端口没有服务"},
	{"context deadline exceeded", "握手超时"},
	{"tls:", "TLS 握手/证书错误"},
	{"EOF", "连接被对端中断"},
}

func logSignatureFindings(in Input) []Finding {
	var problems []string
	for _, l := range strings.Split(in.CoreLog, "\n") {
		if strings.Contains(l, "level=error") || strings.Contains(l, "level=warning") {
			problems = append(problems, l)
		}
	}
	var out []Finding
	for _, s := range logSignatures {
		var hits []string
		for _, l := range problems {
			if strings.Contains(l, s.needle) {
				hits = append(hits, l)
			}
		}
		if len(hits) >= 5 {
			last := hits[len(hits)-1]
			if len(last) > 160 {
				last = last[len(last)-160:]
			}
			out = append(out, Finding{Info, fmt.Sprintf("核心日志里 “%s” 出现 %d 次", s.needle, len(hits)),
				fmt.Sprintf("%s。例：%s", s.meaning, last)})
		}
	}
	return out
}

// MARK: 9. 地理库新旧

func geoFindings(in Input) []Finding {
	if in.GeoDate.IsZero() || in.Now.Sub(in.GeoDate) <= 90*24*time.Hour {
		return nil
	}
	return []Finding{{Info, "内置地理数据库超过 90 天",
		fmt.Sprintf("文件日期 %s。国内 IP 归属可能不再准确，升级 App 即可更新。", FormatTime(in.GeoDate))}}
}

// MARK: 格式化

func FormatTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

// FormatBytes 按 1024 进位，与 macOS 版的 ByteCountFormatter(.binary) 接近："40.05 GB"。
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d 字节", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	switch i {
	case 0:
		return fmt.Sprintf("%.0f %s", value, units[i])
	case 1:
		return fmt.Sprintf("%.1f %s", value, units[i])
	default:
		return fmt.Sprintf("%.2f %s", value, units[i])
	}
}
