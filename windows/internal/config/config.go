// Package config 把"极简设置"翻译成一份固定结构的 mihomo 配置。
//
// 规则阶梯与 macOS 版相同（先匹配先生效）：
//  1. 用户自定义规则          —— 高级用户的覆盖口
//  2. 订阅主机 DIRECT         —— 节点全挂时仍能刷新订阅
//  3. Windows 联网检测 DIRECT —— 节点挂了任务栏也不会显示"无 Internet"（Windows 独有）
//  4. 内网/局域网 DIRECT
//  5. 国内平台与 CDN DIRECT   —— 纯域名，不受 DNS 漂移影响
//  6. 海外固定域名 PROXY      —— 先于 GeoSite，防止数据库过期/DNS 污染
//  7. GeoSite 国内 DIRECT、UDP 443 REJECT（让 QUIC 回退 TCP）、GeoSite 海外 PROXY
//  8. GEOIP,CN DIRECT         —— 兜底
//  9. MATCH PROXY
//
// 与 macOS 版的差异（都是 Windows 网络栈决定的）：
//   - tun.strict-route 打开：mihomo 用随进程消失的 WFP 动态规则拦掉其他网卡上的 53 端口和全部 IPv6 出站，
//     等价于 macOS 版"改系统 DNS + 关 IPv6"，但不修改任何持久设置，核心崩溃也不会把网络留在半改状态。
//   - tun.stack 用 gvisor：system/mixed 栈在 Windows 上会往防火墙里写一条永久的入站规则。
//   - 不写 interface-name：Windows 上的 auto-detect-interface 本来就跳过 Wintun 这类虚拟网卡
//     （Tailscale 也是 Wintun），开了 exit node 也抢不走核心的出站。
package config

import (
	"fmt"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/rules"
	y "github.com/kadaliao/mytizi/windows/internal/yamlx"
)

const (
	ProviderName          = "sub"
	ProviderPath          = "./providers/sub.yaml"
	ProxyGroup            = "PROXY"
	AutoGroup             = "AUTO"
	DefaultHealthCheckURL = "https://www.gstatic.com/generate_204"
)

// Options 是生成配置所需的全部输入。
type Options struct {
	ControllerPort   int
	ControllerSecret string
	// MixedPort 是本机回环上的 HTTP/SOCKS 端口；0 表示不监听。
	MixedPort int
	TUN       bool
	// StrictRoute 打开时拦截其他网卡上的 DNS 与 IPv6（见包注释）。
	StrictRoute bool
	// DirectHosts 是必须直连的域名（订阅所在主机），排在内置规则之前。
	DirectHosts    []string
	CustomRules    []string
	HealthCheckURL string
	// LocalDNS 是原网络的本地 DNS（路由器），只用于 .lan 之类的本地域名。
	LocalDNS []string
	// TailscaleDNS 为 true 时把 *.ts.net 交给 Tailscale 的 MagicDNS（100.100.100.100）解析。
	// strict-route 会拦掉系统发往 Tailscale 网卡的 DNS，只有核心自己去问才问得到。
	TailscaleDNS bool
}

// Build 生成连接用的完整配置。
func Build(o Options) y.Map {
	if o.HealthCheckURL == "" {
		o.HealthCheckURL = DefaultHealthCheckURL
	}
	top := y.Map{}
	if o.MixedPort > 0 {
		top = append(top, y.Pair{Key: "mixed-port", Value: y.Int(o.MixedPort)})
	}
	top = append(top,
		y.Pair{Key: "allow-lan", Value: y.Bool(false)},
		y.Pair{Key: "bind-address", Value: y.String("127.0.0.1")},
		y.Pair{Key: "mode", Value: y.String("rule")},
		y.Pair{Key: "log-level", Value: y.String("info")},
		// IPv6 在三处同时关闭：核心、DNS（不返回 AAAA）、以及 strict-route 在系统层的拦截。
		y.Pair{Key: "ipv6", Value: y.Bool(false)},
		y.Pair{Key: "unified-delay", Value: y.Bool(true)},
		y.Pair{Key: "tcp-concurrent", Value: y.Bool(true)},
		// strict：只有规则里出现 PROCESS-NAME 时才去查进程，自定义规则里的 PROCESS-NAME 才能生效。
		y.Pair{Key: "find-process-mode", Value: y.String("strict")},
		// GeoIP/GeoSite 随 App 内置，禁止核心联网更新，避免首次启动卡在下载上。
		y.Pair{Key: "geo-auto-update", Value: y.Bool(false)},
		y.Pair{Key: "geodata-mode", Value: y.Bool(false)},
		y.Pair{Key: "external-controller", Value: y.String(fmt.Sprintf("127.0.0.1:%d", o.ControllerPort))},
		y.Pair{Key: "secret", Value: y.String(o.ControllerSecret)},
		y.Pair{Key: "profile", Value: y.Map{
			{Key: "store-selected", Value: y.Bool(true)},
			{Key: "store-fake-ip", Value: y.Bool(true)},
		}},
		y.Pair{Key: "tun", Value: tun(o)},
		y.Pair{Key: "dns", Value: dns(o)},
		y.Pair{Key: "sniffer", Value: sniffer()},
		y.Pair{Key: "proxy-providers", Value: y.Map{{Key: ProviderName, Value: y.Map{
			{Key: "type", Value: y.String("file")},
			{Key: "path", Value: y.String(ProviderPath)},
			{Key: "health-check", Value: y.Map{
				{Key: "enable", Value: y.Bool(true)},
				{Key: "url", Value: y.String(o.HealthCheckURL)},
				{Key: "interval", Value: y.Int(600)},
				{Key: "lazy", Value: y.Bool(true)},
			}},
		}}}},
		y.Pair{Key: "proxy-groups", Value: y.Array{
			y.Map{
				{Key: "name", Value: y.String(AutoGroup)},
				{Key: "type", Value: y.String("url-test")},
				{Key: "use", Value: y.Strings([]string{ProviderName})},
				{Key: "url", Value: y.String(o.HealthCheckURL)},
				{Key: "interval", Value: y.Int(300)},
				{Key: "tolerance", Value: y.Int(50)},
				{Key: "lazy", Value: y.Bool(true)},
			},
			y.Map{
				{Key: "name", Value: y.String(ProxyGroup)},
				{Key: "type", Value: y.String("select")},
				{Key: "proxies", Value: y.Strings([]string{AutoGroup})},
				{Key: "use", Value: y.Strings([]string{ProviderName})},
			},
		}},
		y.Pair{Key: "rules", Value: y.Strings(Rules(o))},
	)
	return top
}

func tun(o Options) y.Map {
	return y.Map{
		{Key: "enable", Value: y.Bool(o.TUN)},
		{Key: "device", Value: y.String(appinfo.TunDevice)},
		{Key: "stack", Value: y.String("gvisor")},
		{Key: "auto-route", Value: y.Bool(true)},
		{Key: "auto-detect-interface", Value: y.Bool(true)},
		{Key: "strict-route", Value: y.Bool(o.StrictRoute)},
		{Key: "inet4-address", Value: y.Strings([]string{appinfo.TunGateway + "/30"})},
		// 别的 VPN 自己负责的地址段（Tailscale 的 100.64.0.0/10）在路由层就不进 TUN。
		{Key: "route-exclude-address", Value: y.Strings(rules.TunRouteExclude)},
		{Key: "dns-hijack", Value: y.Strings([]string{"any:53", "tcp://any:53"})},
	}
}

func dns(o Options) y.Map {
	pairs := y.Map{
		{Key: "enable", Value: y.Bool(true)},
		{Key: "ipv6", Value: y.Bool(false)},
		{Key: "use-hosts", Value: y.Bool(true)},
		{Key: "enhanced-mode", Value: y.String("fake-ip")},
		{Key: "fake-ip-range", Value: y.String("198.18.0.1/16")},
		{Key: "fake-ip-filter", Value: y.Strings(rules.FakeIPFilter)},
		{Key: "default-nameserver", Value: y.Strings(rules.DomesticDNS)},
		{Key: "nameserver", Value: y.Strings(rules.DomesticDNS)},
		{Key: "proxy-server-nameserver", Value: y.Strings(rules.DomesticDNS)},
	}
	var policy y.Map
	if local := usableLocalDNS(o.LocalDNS); len(local) > 0 {
		for _, suffix := range []string{"+.lan", "+.home.arpa", "+.localdomain"} {
			policy = append(policy, y.Pair{Key: suffix, Value: y.Strings(local)})
		}
	}
	if o.TailscaleDNS {
		policy = append(policy, y.Pair{Key: "+.ts.net", Value: y.Strings([]string{rules.TailscaleDNS})})
	}
	if len(policy) > 0 {
		pairs = append(pairs, y.Pair{Key: "nameserver-policy", Value: policy})
	}
	return pairs
}

func usableLocalDNS(servers []string) []string {
	var out []string
	for _, s := range servers {
		if len(s) >= 7 && s[:7] == "198.18." || s == "127.0.0.1" || s == "" || s == rules.TailscaleDNS {
			continue
		}
		out = append(out, s)
	}
	return out
}

func sniffer() y.Map {
	return y.Map{
		{Key: "enable", Value: y.Bool(true)},
		{Key: "force-dns-mapping", Value: y.Bool(true)},
		{Key: "parse-pure-ip", Value: y.Bool(true)},
		{Key: "override-destination", Value: y.Bool(true)},
		{Key: "sniff", Value: y.Map{
			{Key: "HTTP", Value: y.Map{{Key: "ports", Value: y.Array{y.Int(80), y.String("8080-8880")}}}},
			{Key: "TLS", Value: y.Map{{Key: "ports", Value: y.Array{y.Int(443), y.Int(8443)}}}},
			{Key: "QUIC", Value: y.Map{{Key: "ports", Value: y.Array{y.Int(443), y.Int(8443)}}}},
		}},
		{Key: "skip-domain", Value: y.Strings(rules.SnifferSkip)},
	}
}

// Rules 返回完整的规则阶梯（顺序由测试固定）。
func Rules(o Options) []string {
	var out []string
	out = append(out, o.CustomRules...)
	for _, host := range o.DirectHosts {
		if host != "" {
			out = append(out, "DOMAIN,"+host+",DIRECT")
		}
	}
	for _, d := range rules.WindowsDirect {
		out = append(out, "DOMAIN-SUFFIX,"+d+",DIRECT")
	}
	out = append(out, "AND,((NETWORK,TCP),(DST-PORT,5223),(IP-CIDR,17.0.0.0/8)),DIRECT")
	for _, cidr := range rules.PrivateIPv4 {
		out = append(out, "IP-CIDR,"+cidr+",DIRECT,no-resolve")
	}
	out = append(out, "GEOSITE,private,DIRECT")
	for _, d := range rules.DirectDomains() {
		out = append(out, "DOMAIN-SUFFIX,"+d+",DIRECT")
	}
	for _, d := range rules.ProxyDomains() {
		out = append(out, "DOMAIN-SUFFIX,"+d+","+ProxyGroup)
	}
	out = append(out,
		"GEOSITE,apple@cn,DIRECT",
		"GEOSITE,microsoft@cn,DIRECT",
		"GEOSITE,cn,DIRECT",
		"AND,((NETWORK,UDP),(DST-PORT,443)),REJECT",
		"GEOSITE,geolocation-!cn,"+ProxyGroup,
		"GEOIP,CN,DIRECT",
		"MATCH,"+ProxyGroup,
	)
	return out
}

// BuildProbe 生成仅用于解析订阅的探测配置：不监听任何代理端口、不启用 TUN/DNS，由 App 以普通用户身份运行。
func BuildProbe(controllerPort int, secret string) y.Map {
	return y.Map{
		{Key: "log-level", Value: y.String("warning")},
		{Key: "ipv6", Value: y.Bool(false)},
		{Key: "geo-auto-update", Value: y.Bool(false)},
		{Key: "external-controller", Value: y.String(fmt.Sprintf("127.0.0.1:%d", controllerPort))},
		{Key: "secret", Value: y.String(secret)},
		{Key: "dns", Value: y.Map{{Key: "enable", Value: y.Bool(false)}}},
		{Key: "proxy-providers", Value: y.Map{{Key: ProviderName, Value: y.Map{
			{Key: "type", Value: y.String("file")},
			{Key: "path", Value: y.String(ProviderPath)},
		}}}},
		{Key: "rules", Value: y.Strings([]string{"MATCH,DIRECT"})},
	}
}
