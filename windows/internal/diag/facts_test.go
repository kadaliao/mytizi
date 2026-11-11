package diag

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// 中文 Windows 11 上连着本 App、同时开着 Tailscale exit node 时的 `route print -4`（节选）。
const routePrintZH = `===========================================================================
接口列表
 18...........................Wintun Userspace Tunnel
 21...........................Tailscale Tunnel
 12...a4 b1 c1 22 33 44 ......Intel(R) Wi-Fi 6E AX211 160MHz
  1...........................Software Loopback Interface 1
===========================================================================

IPv4 路由表
===========================================================================
活动路由:
网络目标        网络掩码          网关       接口   跃点数
          0.0.0.0          0.0.0.0      192.168.2.1    192.168.2.205     35
          0.0.0.0          0.0.0.0            在链路上   100.101.102.103      5
          0.0.0.0        128.0.0.0       198.18.0.2       198.18.0.1      0
        127.0.0.0        255.0.0.0            在链路上         127.0.0.1    331
        128.0.0.0        128.0.0.0       198.18.0.2       198.18.0.1      0
      192.168.2.0    255.255.255.0            在链路上     192.168.2.205    291
       198.18.0.0  255.255.255.252            在链路上        198.18.0.1    256
  255.255.255.255  255.255.255.255            在链路上         127.0.0.1    331
===========================================================================
永久路由:
  网络地址          网络掩码  网关地址  跃点数
          0.0.0.0          0.0.0.0      192.168.2.1     默认
===========================================================================
`

func TestParseRoutePrintChinese(t *testing.T) {
	routes := ParseRoutePrint(routePrintZH)
	var got []string
	for _, r := range routes {
		got = append(got, r.Destination+"/"+itoa(r.PrefixLen)+" "+r.Gateway+" "+r.InterfaceIP+" "+itoa(r.Metric))
	}
	want := []string{
		"0.0.0.0/1 198.18.0.2 198.18.0.1 0",
		"128.0.0.0/1 198.18.0.2 198.18.0.1 0",
		"0.0.0.0/0  100.101.102.103 5", // 在链路上 → 空网关
		"0.0.0.0/0 192.168.2.1 192.168.2.205 35",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseRoutePrintMultiWordOnLink(t *testing.T) {
	// 德语等语言里"在链路上"是两个单词，列数会多一列。
	text := "Aktive Routen:\n     Netzwerkziel    Netzwerkmaske          Gateway    Schnittstelle Metrik\n" +
		"          0.0.0.0          0.0.0.0   Auf Verbindung      10.8.0.6     20\n" +
		"          0.0.0.0          0.0.0.0      192.168.1.1   192.168.1.50     25\n" +
		"Ständige Routen:\n  Keine\n"
	routes := ParseRoutePrint(text)
	if len(routes) != 2 || routes[0].InterfaceIP != "10.8.0.6" || routes[0].Gateway != "" || routes[1].Gateway != "192.168.1.1" {
		t.Fatalf("got %+v", routes)
	}
}

func TestAttachRouteAdapters(t *testing.T) {
	adapters := []Adapter{
		{Name: "WLAN", IPv4: []string{"192.168.2.205"}},
		{Name: "Tailscale", IPv4: []string{"100.101.102.103"}},
	}
	routes := AttachRouteAdapters(ParseRoutePrint(routePrintZH), adapters)
	if routes[2].Adapter != "Tailscale" || routes[3].Adapter != "WLAN" || routes[0].Adapter != "" {
		t.Fatalf("got %+v", routes)
	}
}

func TestParseNRPTJSON(t *testing.T) {
	single := `{"Namespace":".tail1234.ts.net","NameServers":"100.100.100.100"}`
	rules, err := ParseNRPTJSON(single)
	if err != nil || len(rules) != 1 || rules[0].Namespace[0] != ".tail1234.ts.net" || rules[0].NameServers[0] != "100.100.100.100" {
		t.Fatalf("got %+v, %v", rules, err)
	}
	array := "\xef\xbb\xbf" + `[{"Namespace":["corp.example.com",".corp.example.com"],"NameServers":["10.0.0.53","10.0.0.54"]},` +
		`{"Namespace":".ts.net","NameServers":null}]`
	rules, err = ParseNRPTJSON(array)
	if err != nil || len(rules) != 2 || len(rules[0].Namespace) != 2 || len(rules[0].NameServers) != 2 || rules[1].NameServers != nil {
		t.Fatalf("got %+v, %v", rules, err)
	}
	if rules, err := ParseNRPTJSON("  \r\n"); err != nil || rules != nil {
		t.Fatalf("没有规则时应为空：%+v %v", rules, err)
	}
	if _, err := ParseNRPTJSON("Get-DnsClientNrptPolicy : 无法识别"); err == nil {
		t.Error("不是 JSON 时应报错")
	}
}

func TestProxyEndpoints(t *testing.T) {
	cases := map[string][]string{
		"127.0.0.1:7890": {"127.0.0.1:7890"},
		"http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891": {"127.0.0.1:7890", "127.0.0.1:7891"},
		"":                      nil,
		"http://localhost:8080": {"localhost:8080"},
	}
	for input, want := range cases {
		if got := ProxyEndpoints(input); !reflect.DeepEqual(got, want) {
			t.Errorf("ProxyEndpoints(%q) = %v", input, got)
		}
	}
	if !IsLoopbackEndpoint("127.0.0.1:7890") || !IsLoopbackEndpoint("localhost:1") || IsLoopbackEndpoint("10.0.0.1:3128") || IsLoopbackEndpoint("garbage") {
		t.Error("IsLoopbackEndpoint 判断错误")
	}
}

func TestGlobalUnicastIPv6Range(t *testing.T) {
	for _, a := range []string{"2408:8207:1234::5", "240e:306:289a:400:e25d:54ff:fe1a:e967", "2001:db8::1", "3fff::1", "2001:0:2851:782c::1"} {
		if !IsGlobalUnicastIPv6(a) {
			t.Errorf("%s 应是全局地址", a)
		}
	}
	for _, a := range []string{"fd07:b51a:cc66:0:a617:db5e:ab7:e9f1", "FC00::1", "fe80::1%12", "FE80::1", "::1", "::", "ff02::1", "fd::1", "4000::1", "", "not-an-ip", "192.168.1.1"} {
		if IsGlobalUnicastIPv6(a) {
			t.Errorf("%s 不应是全局地址", a)
		}
	}
}

func TestAdapterClassification(t *testing.T) {
	ours := Adapter{Name: "MyTizi", IfType: IfTypePropVirtual, IPv4: []string{"198.18.0.1"}}
	tailscale := Adapter{Name: "Tailscale", Description: "Tailscale Tunnel", IfType: IfTypePropVirtual, IPv4: []string{"100.101.102.103"}}
	tap := Adapter{Name: "以太网 2", Description: "TAP-Windows Adapter V9", IfType: IfTypeEthernet}
	wifi := Adapter{Name: "WLAN", Description: "Intel(R) Wi-Fi 6E AX211 160MHz", IfType: IfTypeIEEE80211, IPv6: []string{"fe80::1", "240e:306::5"}}
	hyperv := Adapter{Name: "vEthernet (Default Switch)", Description: "Hyper-V Virtual Ethernet Adapter", IfType: IfTypeEthernet, IPv6: []string{"fd00::1"}}
	if !ours.IsOurTUN() || tailscale.IsOurTUN() {
		t.Error("本 App 的 TUN 识别错误")
	}
	if !tailscale.IsVirtualTunnel() || !tailscale.IsTailscale() || !tailscale.IsWintunLike() {
		t.Error("Tailscale 识别错误")
	}
	if !tap.IsVirtualTunnel() || tap.IsWintunLike() {
		t.Error("TAP 是虚拟隧道，但不是 Wintun 类型")
	}
	if wifi.IsVirtualTunnel() || hyperv.IsVirtualTunnel() {
		t.Error("物理网卡与 Hyper-V 虚拟交换机不算 VPN")
	}
	if !wifi.HasGlobalIPv6() || hyperv.HasGlobalIPv6() {
		t.Error("全局 IPv6 判断错误")
	}
	wifi.Up, hyperv.Up = true, true
	facts := Facts{Adapters: []Adapter{ours, wifi, hyperv}}
	if got := facts.GlobalIPv6Adapters(); !reflect.DeepEqual(got, []string{"WLAN"}) {
		t.Errorf("got %v", got)
	}
}

func TestConflictingProcesses(t *testing.T) {
	processes := []Process{
		{4, "System"},
		{202, "clash-verge.exe"},
		{203, "verge-mihomo.exe"},
		{303, "sing-box.exe"},
		{404, "mihomo.exe"}, // 本 App 的核心
		{505, "tailscaled.exe"},
		{606, "v2rayN.exe"},
		{707, "clashmate.exe"}, // 名字只是前缀相同，不算
		{808, "WireGuard.exe"},
	}
	found := ConflictingProcesses(processes, map[int]bool{404: true})
	want := []string{"202 clash-verge.exe", "203 verge-mihomo.exe", "303 sing-box.exe", "606 v2rayN.exe", "808 WireGuard.exe"}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("got %v", found)
	}
	if !IsTailscaleProcess("tailscaled.exe") || !IsTailscaleProcess("tailscale-ipn.exe") || IsTailscaleProcess("tailscaler.exe") {
		t.Error("Tailscale 进程识别错误")
	}
}

func TestCGNAT(t *testing.T) {
	if !IsCGNAT("100.64.0.1") || !IsCGNAT("100.127.255.254") || IsCGNAT("100.63.255.255") || IsCGNAT("100.128.0.0") || IsCGNAT("198.18.0.1") {
		t.Error("CGNAT 判断错误")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
