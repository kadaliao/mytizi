package diag

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/redact"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/rules"
	"github.com/kadaliao/mytizi/windows/internal/settings"
)

// Render 把诊断输入渲染成一份可以整段粘贴给 AI 的 Markdown，并在最后统一脱敏。
//
// 拿到这份文本的 AI 不需要任何背景就能开始排查：报告自带"架构说明 + 规则顺序 + 常见现象对照 + 可执行的命令"，
// 并写出每个文件/日志的绝对路径，能读文件的 AI 可以自己继续往下查。
func Render(in Input, full bool) string {
	findings := Findings(in)
	var out []string
	add := func(lines ...string) { out = append(out, lines...) }

	add("# " + appinfo.DisplayName + " 诊断报告")
	header := fmt.Sprintf("生成时间 %s　|　%s %s　|　%s，%s", FormatTime(in.Now), appinfo.DisplayName, orDash(in.AppVersion), orDash(in.Facts.OS), orDash(in.Facts.Arch))
	add(header, "", aiBriefing(full))

	add("## 1. 自动识别的结论")
	if len(findings) == 0 {
		add("没有发现明显异常。请结合用户描述的具体现象，再看下面的自检和日志。")
	}
	for _, f := range findings {
		add(fmt.Sprintf("- **%s　%s**：%s", f.Marker(), f.Title, f.Detail))
	}
	add("")

	add("## 2. 当前状态")
	add(statusSection(in)...)

	add("## 3. 订阅（已脱敏）")
	add(subscriptionSection(in)...)

	if len(in.Nodes) > 0 {
		add("## 4. 线路", "| 名称 | 类型 | 最近延迟 |", "| --- | --- | --- |")
		for i, n := range in.Nodes {
			if i >= 30 {
				add(fmt.Sprintf("（共 %d 个，仅列前 30 个）", len(in.Nodes)))
				break
			}
			delay := "不可用/未测"
			if n.Delay > 0 {
				delay = fmt.Sprintf("%d ms", n.Delay)
			}
			add(fmt.Sprintf("| %s | %s | %s |", n.Name, n.Type, delay))
		}
		add("")
	}

	add("## 5. 实机自检")
	if len(in.SelfTest) == 0 {
		add("本次没有运行自检（未连接时跳过）。")
	} else {
		add("| 项目 | 期望 | 结果 | 实际命中 | 耗时 |", "| --- | --- | --- | --- | --- |")
		for _, t := range in.SelfTest {
			result := "✅ 正常"
			if !t.Passed() {
				switch {
				case t.Concern != "":
					result = "❌ 异常"
				case t.Reachable:
					result = "❌ 走向不符"
				default:
					result = "❌ 不通：" + orText(t.Error, "无响应")
				}
			}
			add(fmt.Sprintf("| %s | %s | %s | %s | %d ms |", t.Name, orDash(string(t.Expected)), result, orDash(t.Observed), t.Milliseconds))
		}
	}
	add("")

	add("## 6. 系统网络")
	add(networkSection(in)...)

	add("## 7. 规则")
	add(rulesSection(in)...)

	add("## 8. 文件位置（能读文件的 AI 可以直接查看）")
	add(filesSection(in)...)

	add("## 9. 日志")
	add(logsSection(in, full)...)

	return makeRedactor(in).Redact(strings.Join(out, "\n"))
}

// makeRedactor 登记必须抹掉的秘密：订阅链接（含 token）、控制接口密钥。
func makeRedactor(in Input) *redact.Redactor {
	r := redact.New("")
	if in.Settings.Subscription != nil {
		r.AddSubscriptionURL(in.Settings.Subscription.URL)
	}
	if in.Status != nil {
		r.Add(in.Status.ControllerSecret, "控制密钥")
	}
	return r
}

func aiBriefing(full bool) string {
	privacy := "这是摘要版，不含连接日志。"
	if full {
		privacy = "这是**完整版**，日志里有用户访问过的域名，发给别人之前请自行确认。"
	}
	lines := []string{
		"## 给 AI 的说明",
		appinfo.DisplayName + " 是 Windows 上的极简 TUN 代理客户端，内置 mihomo 核心。请基于下面的信息帮我排查问题：先给结论，再给我可以逐条执行的步骤（PowerShell）；需要更多信息时告诉我执行哪条命令。",
		"",
		"**架构要点**",
		fmt.Sprintf("1. 安装程序注册了一个以 LocalSystem 运行的 Windows 服务 `%s`，App 通过命名管道 `%s` 让它启停 mihomo。配置由服务根据订阅原文和少量经过校验的设置生成，App 不能直接下发配置。", appinfo.ServiceName, appinfo.PipeName),
		fmt.Sprintf("2. mihomo 创建名为 `%s` 的 Wintun 虚拟网卡，地址 `%s`，网卡 DNS 为 `%s`，fake-ip 地址段 `198.18.0.0/16`，协议栈 gvisor。`auto-detect-interface` 选择出口时跳过 Wintun 类网卡（包括 Tailscale）。", appinfo.TunDevice, appinfo.TunGateway, appinfo.TunDNS),
		"3. 「严格接管」= mihomo 的 `tun.strict-route`：用 WFP 动态规则拦截其他网卡上的 53 端口和全部 IPv6 出站。这些规则、虚拟网卡和路由都随 mihomo 进程一起消失，**不修改任何持久的系统设置**，所以没有需要还原的恢复点；服务用作业对象保证服务退出时核心也退出。",
		"4. 规则顺序（先匹配先生效）：自定义 → 订阅主机直连 → Windows 联网检测（msftconnecttest / msftncsi）直连 → 内网直连 → 国内平台与 CDN 域名直连 → 海外固定域名走代理 → GeoSite 国内直连 → UDP 443 拒绝（迫使 QUIC 回退到 TCP）→ GeoSite 海外走代理 → `GEOIP,CN` 直连（兜底）→ 其余走代理。",
		"5. 订阅由 App 抓取后交给 mihomo 作为 file provider 解析；订阅自带的规则不会被使用。GeoIP/GeoSite 数据库随安装包内置，核心不会联网更新。",
		"6. 核心日志里 `match X using Y` 表示该连接命中的规则和出口：`DIRECT` 是直连，`PROXY[…]` 是走代理，`REJECT` 是拒绝。",
		"7. Tailscale 可以共存：100.64.0.0/10 不进本 App 的 TUN，*.ts.net 由 mihomo 交给 100.100.100.100 解析；但开启 exit node 会接管默认路由，诊断会单独指出。",
		"",
		"**常见现象 → 可能原因**",
		"- 国内网站 / 微信图片 / 飞书打不开或很慢 → 命中了 `PROXY`（在日志里搜该域名），需要补直连域名；或 DNS 没被接管（自检第一行不是 fake-ip）。",
		"- 海外网站打不开 → 命中了 `DIRECT`，或所有线路延迟都失败（节点不可用 / 订阅过期）。",
		"- 日志里大量 `dial PROXY … context deadline exceeded`，而直连自检正常 → **节点服务器连不上**（不是规则问题）：先查节点地址/端口、DDNS、IPv6。",
		"- 日志里 `dial PROXY` 伴随 `tls:` / `EOF` / 认证错误 → 节点能连上但协议或密码/UUID 不对。",
		"- 全部打不开 → 后台服务没运行、另一个代理也开了 TUN、系统代理指向已关闭的端口、杀毒软件拦截了 mihomo.exe 或 Wintun 驱动。",
		"- 公司内网域名解析失败 → 系统里有 NRPT 策略（DirectAccess 等），被「严格接管」拦掉了。",
		"- 断开后断网 → 检查 `MyTizi` 网卡是否还在（`Get-NetAdapter`），然后 `ipconfig /flushdns`。",
		"",
		"**隐私**：订阅链接、控制密钥、节点密码与 UUID 已移除。" + privacy,
	}
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func statusSection(in Input) []string {
	var lines []string
	phase := "- App 阶段：" + orDash(in.AppPhase)
	if in.LastError != "" {
		phase += "；最近错误：" + in.LastError
	}
	lines = append(lines, phase)
	lines = append(lines, fmt.Sprintf("- 服务 %s：%s", appinfo.ServiceName, in.ServiceState.Describe()))
	if s := in.Status; s != nil {
		parts := []string{"阶段 " + string(s.Phase)}
		if s.CorePID > 0 {
			parts = append(parts, fmt.Sprintf("核心 pid %d", s.CorePID))
		}
		if s.StartedAt != nil {
			parts = append(parts, "已运行 "+duration(in.Now.Sub(*s.StartedAt).Seconds()))
		}
		if s.TunInterface != "" {
			parts = append(parts, "TUN "+s.TunInterface)
		}
		if s.Phase == "running" {
			parts = append(parts, fmt.Sprintf("严格接管 %v", s.StrictRoute))
		}
		if s.ControllerPort > 0 {
			parts = append(parts, fmt.Sprintf("控制端口 127.0.0.1:%d", s.ControllerPort))
		}
		lines = append(lines, "- 后台组件："+strings.Join(parts, "，"))
		if e := s.LastExit; e != nil {
			kind := "非预期"
			if e.Expected {
				kind = "预期内"
			}
			lines = append(lines, fmt.Sprintf("- 核心上次退出：%s，code %d，%s", FormatTime(e.At), e.Code, kind))
		}
	} else {
		text := "- 后台组件：无法连接"
		if in.ServiceError != "" {
			text += "（" + in.ServiceError + "）"
		}
		lines = append(lines, text)
	}
	if h := in.Hello; h != nil {
		lines = append(lines, fmt.Sprintf("- 后台组件版本 %s，协议 %d，pid %d，核心已安装 %v，安装目录 %s",
			orDash(h.ServiceVersion), h.IPCVersion, h.ServicePID, h.CoreInstalled, h.InstallDir))
	}
	if in.CoreVersion != "" {
		lines = append(lines, "- 核心版本："+in.CoreVersion)
	}
	if !in.GeoDate.IsZero() {
		lines = append(lines, "- 内置地理库文件日期："+FormatTime(in.GeoDate))
	}
	lines = append(lines, fmt.Sprintf("- 设置：严格接管 %v，打开时自动连接 %v，测速地址 %s",
		in.Settings.StrictRoute, in.Settings.AutoConnectOnLaunch, orText(in.Settings.HealthCheckURL, config.DefaultHealthCheckURL)))
	return append(lines, "")
}

func subscriptionSection(in Input) []string {
	sub := in.Settings.Subscription
	if sub == nil {
		return []string{"尚未添加订阅。", ""}
	}
	updated := "从未"
	if sub.UpdatedAt != nil {
		updated = FormatTime(*sub.UpdatedAt)
	}
	lines := []string{
		fmt.Sprintf("- 来源主机：%s（完整链接已隐藏）", sub.Host()),
		"- 名称：" + sub.Name,
		fmt.Sprintf("- 线路数：%d；协议分布：%s", len(sub.Nodes), typeBreakdown(sub.Nodes)),
		fmt.Sprintf("- 添加于 %s；上次更新 %s", FormatTime(sub.AddedAt), updated),
	}
	if u := sub.Usage; u != nil {
		total, expire := "不限", "不过期"
		if u.Total > 0 {
			total = FormatBytes(u.Total)
		}
		if u.Expire != nil {
			expire = FormatTime(*u.Expire)
		}
		lines = append(lines, fmt.Sprintf("- 流量：已用 %s / %s；到期 %s", FormatBytes(u.Used()), total, expire))
	}
	return append(lines, "")
}

func networkSection(in Input) []string {
	f := in.Facts
	var lines []string
	if len(f.DefaultRoutes) == 0 {
		lines = append(lines, "- 默认路由：未知")
	} else {
		var routes []string
		for _, r := range f.DefaultRoutes {
			gateway := r.Gateway
			if gateway == "" {
				gateway = "在链路上"
			}
			routes = append(routes, fmt.Sprintf("%s/%d → %s via %s（接口 %s，跃点 %d）",
				r.Destination, r.PrefixLen, orText(r.Adapter, "?"), gateway, r.InterfaceIP, r.Metric))
		}
		lines = append(lines, "- 默认路由（按系统选择顺序）："+strings.Join(routes, "；"))
	}
	lines = append(lines, "- 网卡（只列已启用的）：")
	listed := 0
	for _, a := range f.Adapters {
		if !a.Up || a.IfType == IfTypeLoopback {
			continue
		}
		listed++
		tags := []string{}
		if a.IsOurTUN() {
			tags = append(tags, "本 App 的 TUN")
		} else if a.IsTailscale() {
			tags = append(tags, "Tailscale")
		} else if a.IsVirtualTunnel() {
			tags = append(tags, "虚拟隧道")
		}
		if a.HasGlobalIPv6() {
			tags = append(tags, "有公网 IPv6")
		}
		tag := ""
		if len(tags) > 0 {
			tag = " [" + strings.Join(tags, "，") + "]"
		}
		lines = append(lines, fmt.Sprintf("  - %s（%s，类型 %d，跃点 %d）%s：IPv4 %s；DNS %s；网关 %s",
			a.Name, orDash(a.Description), a.IfType, a.Metric, tag,
			orDash(strings.Join(a.IPv4, ", ")), orDash(strings.Join(a.DNS, ", ")), orDash(strings.Join(a.Gateways, ", "))))
	}
	if listed == 0 {
		lines = append(lines, "  - （没有读到网卡信息）")
	}
	p := f.Proxy
	switch {
	case p.Active():
		text := fmt.Sprintf("- 系统代理：启用 %v，服务器 %s，自动配置 %s", p.Enabled, orDash(p.Server), orDash(p.AutoConfigURL))
		if len(p.DeadEndpoints) > 0 {
			text += "，⚠️ 无程序监听：" + strings.Join(p.DeadEndpoints, ", ")
		}
		lines = append(lines, text)
	default:
		lines = append(lines, "- 系统代理：未开启")
	}
	switch {
	case f.NRPTError != "":
		lines = append(lines, "- NRPT（按域名指定 DNS）：读取失败（"+f.NRPTError+"）")
	case len(f.NRPT) == 0:
		lines = append(lines, "- NRPT（按域名指定 DNS）：无")
	default:
		var rulesText []string
		for _, r := range f.NRPT {
			rulesText = append(rulesText, strings.Join(r.Namespace, ",")+" → "+orDash(strings.Join(r.NameServers, ",")))
		}
		lines = append(lines, "- NRPT（按域名指定 DNS）："+strings.Join(rulesText, "；"))
	}
	if v6 := f.GlobalIPv6Adapters(); len(v6) > 0 {
		lines = append(lines, "- 有公网 IPv6 地址的网卡："+strings.Join(v6, ", "))
	} else {
		lines = append(lines, "- 有公网 IPv6 地址的网卡：无")
	}
	tailscale := "未发现"
	if f.TailscalePresent() {
		tailscale = "在运行"
	}
	lines = append(lines, "- Tailscale："+tailscale)
	others := "未发现"
	if len(f.OtherProxyProcesses) > 0 {
		list := f.OtherProxyProcesses
		if len(list) > 8 {
			list = list[:8]
		}
		others = strings.Join(list, "；")
	}
	lines = append(lines, "- 其他代理/VPN 进程："+others)
	return append(lines, "")
}

func rulesSection(in Input) []string {
	all := config.Rules(config.Options{})
	return []string{
		fmt.Sprintf("- 共 %d 条内置规则（国内平台直连域名 %d 个，海外固定代理域名 %d 个），另有自定义规则 %d 条。",
			len(all), len(rules.DirectDomains()), len(rules.ProxyDomains()), in.CustomRuleCount),
		fmt.Sprintf("- 最近一次生成的配置（已脱敏）：`%s`", in.userPaths().LastConfigRedacted()),
		fmt.Sprintf("- 兜底：`GEOIP,CN → DIRECT`，其余 `MATCH → PROXY`；DNS 为 fake-ip，国内 DNS %s。", strings.Join(rules.DomesticDNS, " / ")),
		"",
	}
}

func (in Input) userPaths() paths.User {
	if in.UserPaths.Support == "" {
		return paths.DefaultUser()
	}
	return in.UserPaths
}

func filesSection(in Input) []string {
	sys := in.system()
	user := in.userPaths()
	install := orText(in.InstallDir, paths.InstallDir())
	core := paths.ComponentsIn(install).Core()
	return []string{
		"| 内容 | 路径 |",
		"| --- | --- |",
		fmt.Sprintf("| 核心日志（含每条连接命中的规则） | `%s` |", sys.CoreLog()),
		fmt.Sprintf("| 后台服务日志 | `%s` |", sys.HelperLog()),
		fmt.Sprintf("| App 日志 | `%s` |", user.AppLog()),
		fmt.Sprintf("| 设置（含订阅链接，勿外发） | `%s` |", user.Settings()),
		fmt.Sprintf("| 自定义规则 | `%s` |", user.CustomRules()),
		fmt.Sprintf("| 最近配置（脱敏） | `%s` |", user.LastConfigRedacted()),
		fmt.Sprintf("| 服务生成的配置（需管理员读取，含控制密钥） | `%s` |", sys.ConfigPath()),
		fmt.Sprintf("| 安装目录 / 内置核心 | `%s` / `%s` |", install, core),
		"",
		"常用排查命令（PowerShell）：",
		"```powershell",
		fmt.Sprintf(`Get-Content "$env:ProgramData\%s\logs\mihomo.log" -Tail 200`, appinfo.DisplayName),
		fmt.Sprintf(`Get-Content "$env:ProgramData\%s\logs\helper.log" -Tail 100`, appinfo.DisplayName),
		"sc.exe query " + appinfo.ServiceName,
		"Get-NetAdapter",
		"Get-DnsClientServerAddress -AddressFamily IPv4",
		"Get-NetRoute -DestinationPrefix 0.0.0.0/0",
		"Get-DnsClientNrptPolicy",
		"ipconfig /all",
		"netsh winhttp show proxy",
		"```",
		"",
	}
}

func logsSection(in Input, full bool) []string {
	var lines []string
	block := func(title, text string) {
		lines = append(lines, "### "+title, "```", orText(text, "（空）"), "```", "")
	}
	block("App 日志（末尾 30 行）", rotlog.LastLines(in.AppLog, 30))
	block("后台服务日志（末尾 40 行）", rotlog.LastLines(in.HelperLog, 40))
	var problems []string
	for _, l := range strings.Split(in.CoreLog, "\n") {
		if strings.Contains(l, "level=error") || strings.Contains(l, "level=warning") || strings.Contains(l, "level=fatal") {
			problems = append(problems, strings.TrimRight(l, "\r"))
		}
	}
	if len(problems) > 40 {
		problems = problems[len(problems)-40:]
	}
	block("核心日志：警告与错误（末尾 40 条）", strings.Join(problems, "\n"))
	if full {
		block("核心日志：最近 200 行（含访问过的域名）", rotlog.LastLines(in.CoreLog, 200))
	}
	return lines
}

func typeBreakdown(nodes []settings.Node) string {
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.Type]++
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	var parts []string
	for _, e := range list {
		parts = append(parts, fmt.Sprintf("%s×%d", e.k, e.v))
	}
	return orDash(strings.Join(parts, " "))
}

func duration(seconds float64) string {
	s := int(seconds)
	if s < 0 {
		s = 0
	}
	switch {
	case s >= 3600:
		return fmt.Sprintf("%d 小时 %d 分", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%d 分 %d 秒", s/60, s%60)
	default:
		return fmt.Sprintf("%d 秒", s)
	}
}

func orDash(s string) string { return orText(s, "-") }

func orText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// geoDate 是安装目录里地理库文件的修改时间（安装包保留了构建时的文件时间）。
func geoFile(installDir string) string {
	return filepath.Join(paths.ComponentsIn(installDir).Dir, "geoip.metadb")
}
