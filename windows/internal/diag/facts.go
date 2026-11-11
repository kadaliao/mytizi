package diag

import (
	"encoding/json"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

// 诊断用的系统事实：全部是只读操作，每一项失败都只会让对应字段为空。
// 解析函数是纯函数，用真实输出做测试样本，在 macOS 上也能跑。

// Windows 的网卡类型（IANA ifType）。
const (
	IfTypeEthernet    = 6
	IfTypeLoopback    = 24
	IfTypeIEEE80211   = 71
	IfTypeTunnel      = 131
	IfTypePropVirtual = 53 // Wintun（本 App、Tailscale、WireGuard）都是这个类型
)

// Adapter 是一块网卡（GetAdaptersAddresses 的一项）。
type Adapter struct {
	Name        string // 友好名称，例如 "WLAN"、"MyTizi"
	Description string // 驱动描述，例如 "Wintun Userspace Tunnel"
	IfType      uint32
	Up          bool
	IPv4        []string
	IPv6        []string
	DNS         []string
	Gateways    []string
	Metric      uint32 // IPv4 接口跃点数
}

// IsOurTUN：名字是 MyTizi，或者持有 198.18.0.1。
func (a Adapter) IsOurTUN() bool {
	return a.Name == appinfo.TunDevice || contains(a.IPv4, appinfo.TunGateway)
}

var tunnelWords = []string{"wintun", "tap-windows", "tap-", "wireguard", "tailscale", "openvpn", "zerotier", "vpn"}

// IsVirtualTunnel：别的 VPN/代理的虚拟网卡（Wintun 类型，或者描述里带 TAP/WireGuard 等字样）。
func (a Adapter) IsVirtualTunnel() bool {
	if a.IfType == IfTypePropVirtual {
		return true
	}
	text := strings.ToLower(a.Name + " " + a.Description)
	for _, w := range tunnelWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// IsWintunLike：mihomo 自动选择出口时会跳过的那类虚拟网卡。
func (a Adapter) IsWintunLike() bool { return a.IfType == IfTypePropVirtual }

// IsTailscale：Tailscale 的网卡（名字或描述里有 Tailscale，或地址在 100.64.0.0/10）。
func (a Adapter) IsTailscale() bool {
	if strings.Contains(strings.ToLower(a.Name+" "+a.Description), "tailscale") {
		return true
	}
	for _, ip := range a.IPv4 {
		if IsCGNAT(ip) {
			return true
		}
	}
	return false
}

// HasGlobalIPv6 只算能上公网的全局单播地址（2000::/3）。
func (a Adapter) HasGlobalIPv6() bool {
	for _, ip := range a.IPv6 {
		if IsGlobalUnicastIPv6(ip) {
			return true
		}
	}
	return false
}

// DefaultRoute 是 route print 里的一条默认路由（0.0.0.0/0，或 VPN 常用的 0.0.0.0/1、128.0.0.0/1）。
type DefaultRoute struct {
	Destination string
	PrefixLen   int
	// Gateway 是网关地址；"在链路上"（On-link）之类的本地化文字统一记为空。
	Gateway     string
	InterfaceIP string
	Adapter     string // 由 InterfaceIP 对应到网卡名；对不上时为空
	Metric      int
}

// ProxyInfo 是当前用户的系统代理（Internet 选项）。
type ProxyInfo struct {
	Enabled       bool
	Server        string
	AutoConfigURL string
	// DeadEndpoints 是指向本机、但没有程序在监听的代理地址。
	DeadEndpoints []string
}

func (p ProxyInfo) Active() bool { return (p.Enabled && p.Server != "") || p.AutoConfigURL != "" }

// NRPTRule 是一条名称解析策略（某些域名交给指定 DNS），Tailscale MagicDNS、DirectAccess 会写这里。
type NRPTRule struct {
	Namespace   []string
	NameServers []string
}

// Facts 是一次采集的快照。
type Facts struct {
	OS            string
	Arch          string
	Adapters      []Adapter
	DefaultRoutes []DefaultRoute
	Proxy         ProxyInfo
	NRPT          []NRPTRule
	NRPTError     string
	// OtherProxyProcesses 形如 "1234 clash-verge.exe"。
	OtherProxyProcesses []string
	TailscaleRunning    bool
	Timestamp           time.Time
}

// OurTUN 返回本 App 的虚拟网卡。
func (f Facts) OurTUN() *Adapter {
	for i := range f.Adapters {
		if f.Adapters[i].IsOurTUN() {
			return &f.Adapters[i]
		}
	}
	return nil
}

// Adapter 按名字查网卡。
func (f Facts) Adapter(name string) *Adapter {
	for i := range f.Adapters {
		if f.Adapters[i].Name == name {
			return &f.Adapters[i]
		}
	}
	return nil
}

// RivalTUNs 是持有 198.18.x.x 地址的别的网卡（另一个代理也开了 TUN）。
func (f Facts) RivalTUNs() []Adapter {
	var out []Adapter
	for _, a := range f.Adapters {
		if a.Name == appinfo.TunDevice || !a.Up {
			continue
		}
		for _, ip := range a.IPv4 {
			if strings.HasPrefix(ip, "198.18.") && ip != appinfo.TunGateway {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// GlobalIPv6Adapters 是有公网 IPv6 地址、且不属于本 App 的已启用网卡。
func (f Facts) GlobalIPv6Adapters() []string {
	var out []string
	for _, a := range f.Adapters {
		if a.Up && !a.IsOurTUN() && a.IfType != IfTypeLoopback && a.HasGlobalIPv6() {
			out = append(out, a.Name)
		}
	}
	return out
}

// TailscalePresent：Tailscale 进程在运行或者它的网卡在线。
func (f Facts) TailscalePresent() bool {
	if f.TailscaleRunning {
		return true
	}
	for _, a := range f.Adapters {
		if a.Up && a.IsTailscale() {
			return true
		}
	}
	return false
}

// MARK: 解析

// ParseRoutePrint 从 `route print -4` 的输出里取出默认路由。表头和"在链路上"都是本地化的，
// 所以只认数字列：目标 掩码 网关 接口 跃点数（网关可能是一个或几个本地化单词）。
// "永久路由"一节只有四列，自然被跳过。结果按前缀长度（长的优先）再按跃点数排序，即系统实际的选择顺序。
func ParseRoutePrint(text string) []DefaultRoute {
	var out []DefaultRoute
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		prefix := 0
		switch fields[0] + "/" + fields[1] {
		case "0.0.0.0/0.0.0.0":
			prefix = 0
		case "0.0.0.0/128.0.0.0", "128.0.0.0/128.0.0.0":
			prefix = 1
		default:
			continue
		}
		last := len(fields) - 1
		metric, err := strconv.Atoi(fields[last])
		if err != nil {
			continue
		}
		ifIP, err := netip.ParseAddr(fields[last-1])
		if err != nil || !ifIP.Is4() {
			continue
		}
		gateway := strings.Join(fields[2:last-1], " ")
		if addr, err := netip.ParseAddr(gateway); err != nil || !addr.Is4() {
			gateway = ""
		}
		out = append(out, DefaultRoute{
			Destination: fields[0], PrefixLen: prefix, Gateway: gateway, InterfaceIP: ifIP.String(), Metric: metric,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PrefixLen != out[j].PrefixLen {
			return out[i].PrefixLen > out[j].PrefixLen
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// AttachRouteAdapters 用网卡的 IPv4 地址把路由对应到网卡名。
func AttachRouteAdapters(routes []DefaultRoute, adapters []Adapter) []DefaultRoute {
	out := append([]DefaultRoute(nil), routes...)
	for i := range out {
		for _, a := range adapters {
			if contains(a.IPv4, out[i].InterfaceIP) {
				out[i].Adapter = a.Name
				break
			}
		}
	}
	return out
}

// stringList 兼容 ConvertTo-Json 把单元素数组输出成字符串的习惯。
type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		if one != "" {
			*s = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return nil // null 或别的形状：当作空
	}
	*s = many
	return nil
}

// ParseNRPTJSON 解析 `Get-DnsClientNrptPolicy | Select-Object Namespace,NameServers | ConvertTo-Json`：
// 没有规则时输出为空；一条规则是对象，多条是数组。
func ParseNRPTJSON(text string) ([]NRPTRule, error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "\xef\xbb\xbf"))
	if text == "" {
		return nil, nil
	}
	type raw struct {
		Namespace   stringList `json:"Namespace"`
		NameServers stringList `json:"NameServers"`
	}
	var list []raw
	if strings.HasPrefix(text, "[") {
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return nil, err
		}
	} else {
		var one raw
		if err := json.Unmarshal([]byte(text), &one); err != nil {
			return nil, err
		}
		list = []raw{one}
	}
	var out []NRPTRule
	for _, r := range list {
		out = append(out, NRPTRule{Namespace: r.Namespace, NameServers: r.NameServers})
	}
	return out, nil
}

// ProxyEndpoints 拆开 Internet 选项里的 ProxyServer：可能是 "127.0.0.1:7890"，
// 也可能是 "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891"。
func ProxyEndpoints(server string) []string {
	var out []string
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i >= 0 {
			part = strings.TrimSpace(part[i+1:])
		}
		part = strings.TrimPrefix(strings.TrimPrefix(part, "http://"), "socks://")
		if part != "" && !containsString(out, part) {
			out = append(out, part)
		}
	}
	return out
}

// IsLoopbackEndpoint：host:port 指向本机回环。
func IsLoopbackEndpoint(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// IsGlobalUnicastIPv6 是否属于全局单播 2000::/3（公网 IPv6 都在这个段里）。
// 链路本地（fe80::/10）和 ULA（fc00::/7，Hyper-V、WSL 的虚拟网卡常用）出不了本机。
func IsGlobalUnicastIPv6(address string) bool {
	bare := strings.SplitN(address, "%", 2)[0]
	head := strings.SplitN(bare, ":", 2)[0]
	if head == "" || len(head) > 4 || !strings.Contains(bare, ":") {
		return false
	}
	value, err := strconv.ParseUint(head, 16, 16)
	if err != nil {
		return false
	}
	return value&0xE000 == 0x2000
}

// IsCGNAT：100.64.0.0/10，Tailscale 给每台设备分配的地址段。
func IsCGNAT(address string) bool {
	addr, err := netip.ParseAddr(address)
	return err == nil && netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}

// ConflictingProcessNames 是已知会与本 App 的 TUN 冲突的代理/VPN 程序（按可执行文件名匹配）。
// Tailscale 不在这里：它可以共存，真正会冲突的情况由路由与 DNS 检查单独判断。
var ConflictingProcessNames = []string{
	"clash-verge", "verge-mihomo", "mihomo", "clash", "clash-meta", "clash-for-windows", "flclash",
	"sing-box", "v2rayn", "v2ray", "xray", "nekoray", "nekobox", "hiddify", "wireguard", "openvpn",
	"openvpn-gui", "hysteria", "tuic", "naive", "sstap", "netch", "privoxy", "shadowsocks",
}

// Process 是进程快照里的一项。
type Process struct {
	PID  int
	Name string // 例如 "clash-verge.exe"
}

// ConflictingProcesses 找出冲突的进程，排除 excludePIDs（本 App 自己的核心）。
func ConflictingProcesses(processes []Process, excludePIDs map[int]bool) []string {
	var found []string
	for _, p := range processes {
		if excludePIDs[p.PID] {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(strings.ToLower(p.Name), ".exe"))
		for _, name := range ConflictingProcessNames {
			if base == name || strings.HasPrefix(base, name+"-") || strings.HasPrefix(base, name+".") || strings.HasPrefix(base, name+"_") {
				found = append(found, strconv.Itoa(p.PID)+" "+p.Name)
				break
			}
		}
	}
	return found
}

// IsTailscaleProcess：tailscaled.exe / tailscale-ipn.exe。
func IsTailscaleProcess(name string) bool {
	base := strings.ToLower(strings.TrimSuffix(strings.ToLower(name), ".exe"))
	return base == "tailscaled" || base == "tailscale-ipn" || base == "tailscale"
}

func contains(list []string, item string) bool { return containsString(list, item) }

func containsString(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
