package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kadaliao/mytizi/windows/internal/testutil"
	y "github.com/kadaliao/mytizi/windows/internal/yamlx"
)

func TestYAMLScalarsAreAlwaysQuoted(t *testing.T) {
	got := y.Render(y.Map{{"mode", y.String("no")}, {"a", y.String("198.18.0.1/16")}, {"n", y.Int(7)}, {"b", y.Bool(true)}})
	want := "mode: \"no\"\na: \"198.18.0.1/16\"\nn: 7\nb: true\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestYAMLNesting(t *testing.T) {
	got := y.Render(y.Map{
		{"+.lan", y.Strings([]string{"1.1.1.1"})},
		{"tun", y.Map{{"enable", y.Bool(true)}, {"dns-hijack", y.Strings([]string{"any:53"})}}},
		{"groups", y.Array{
			y.Map{{"name", y.String("A")}, {"use", y.Strings([]string{"sub"})}},
			y.Map{{"name", y.String("B")}, {"proxies", y.Strings(nil)}},
		}},
		{"empty", y.Map{}},
	})
	want := `"+.lan":
  - "1.1.1.1"
tun:
  enable: true
  dns-hijack:
    - "any:53"
groups:
  - name: "A"
    use:
      - "sub"
  - name: "B"
    proxies: []
empty: {}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestQuoteEscapes(t *testing.T) {
	if got := y.Quote("a\"b\\c\nd\x01"); got != `"a\"b\\c\nd\u0001"` {
		t.Fatalf("got %s", got)
	}
	if got := y.Quote("节点-香港"); got != `"节点-香港"` {
		t.Fatalf("got %s", got)
	}
}

func indexOf(list []string, item string) int {
	for i, v := range list {
		if v == item {
			return i
		}
	}
	return -1
}

func TestRuleLadderOrder(t *testing.T) {
	rules := Rules(Options{
		CustomRules: []string{"DOMAIN-SUFFIX,custom.example,DIRECT"},
		DirectHosts: []string{"sub.example.com"},
	})
	order := []string{
		"DOMAIN-SUFFIX,custom.example,DIRECT",
		"DOMAIN,sub.example.com,DIRECT",
		"DOMAIN-SUFFIX,msftconnecttest.com,DIRECT",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"GEOSITE,private,DIRECT",
		"DOMAIN-SUFFIX,qq.com,DIRECT",
		"DOMAIN-SUFFIX,youtube.com,PROXY",
		"GEOSITE,cn,DIRECT",
		"AND,((NETWORK,UDP),(DST-PORT,443)),REJECT",
		"GEOSITE,geolocation-!cn,PROXY",
		"GEOIP,CN,DIRECT",
		"MATCH,PROXY",
	}
	last := -1
	for _, rule := range order {
		i := indexOf(rules, rule)
		if i < 0 {
			t.Fatalf("缺少规则 %s", rule)
		}
		if i <= last {
			t.Fatalf("规则 %s 的位置不对（%d ≤ %d）", rule, i, last)
		}
		last = i
	}
	if rules[len(rules)-1] != "MATCH,PROXY" {
		t.Fatal("MATCH 必须是最后一条")
	}
}

func TestWindowsSpecificTunSettings(t *testing.T) {
	text := y.Render(Build(Options{ControllerPort: 9097, ControllerSecret: "s3cret-value", TUN: true, StrictRoute: true}))
	for _, want := range []string{
		"  stack: \"gvisor\"",
		"  strict-route: true",
		"  device: \"MyTizi\"",
		"  auto-detect-interface: true",
		"    - \"198.18.0.1/30\"",
		"    - \"100.64.0.0/10\"",
		"ipv6: false",
		"enhanced-mode: \"fake-ip\"",
		"external-controller: \"127.0.0.1:9097\"",
		"find-process-mode: \"strict\"",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("配置里缺少 %q", want)
		}
	}
	if strings.Contains(text, "interface-name") {
		t.Error("Windows 版不应固定 interface-name")
	}
	if strings.Contains(text, "mixed-port") {
		t.Error("没有要求时不应监听 mixed-port")
	}
}

func TestDNSPolicy(t *testing.T) {
	text := y.Render(Build(Options{LocalDNS: []string{"192.168.1.1", "198.18.0.2", "127.0.0.1"}, TailscaleDNS: true}))
	if !strings.Contains(text, "\"+.lan\":\n      - \"192.168.1.1\"") {
		t.Errorf("本地域名应交给路由器 DNS：\n%s", text)
	}
	if strings.Contains(text, "- \"198.18.0.2\"\n") {
		t.Error("TUN 自己的 DNS 不能出现在 nameserver-policy 里")
	}
	if !strings.Contains(text, "\"+.ts.net\":\n      - \"100.100.100.100\"") {
		t.Error("开着 Tailscale 时 ts.net 应交给 MagicDNS")
	}
	plain := y.Render(Build(Options{}))
	if strings.Contains(plain, "nameserver-policy") {
		t.Error("没有本地 DNS 也没有 Tailscale 时不应生成 nameserver-policy")
	}
}

func TestCustomRules(t *testing.T) {
	rules, err := ParseCustomRules("\ufeff# 注释\r\ndomain-suffix, example.com , 代理 # 行尾说明\r\nIP-CIDR,10.1.0.0/16,DIRECT,no-resolve\r\nPROCESS-NAME,WeChat.exe,直连\r\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DOMAIN-SUFFIX,example.com,PROXY", "IP-CIDR,10.1.0.0/16,DIRECT,no-resolve", "PROCESS-NAME,WeChat.exe,DIRECT"}
	if strings.Join(rules, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v", rules)
	}
	bad := map[string]string{
		"DOMAIN,example.com":                      "第 1 行",
		"\nRULE-SET,x,PROXY":                      "第 2 行",
		"DOMAIN,example.com,AUTO":                 "目标必须",
		"DOMAIN,example.com,DIRECT,no-resolve":    "多余的参数",
		"DOMAIN,,DIRECT":                          "缺少匹配值",
		"AND,((DOMAIN,x)),DIRECT":                 "不支持",
		"DOMAIN-KEYWORD,a(b,DIRECT":               "不允许的字符",
		"IP-CIDR,1.2.3.0/24,DIRECT,no-resolve,xx": "多余的参数",
	}
	for input, message := range bad {
		_, err := ParseCustomRules(input)
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("%q 应报错包含 %q，实际 %v", input, message, err)
		}
	}
}

// 生成的配置必须通过真实 mihomo 的 -t 校验（与 macOS 版的测试相同）。
func TestGeneratedConfigPassesCoreValidation(t *testing.T) {
	core := testutil.RequireCore(t)
	home := testutil.CoreHome(t)
	if err := os.WriteFile(filepath.Join(home, "providers", "sub.yaml"), []byte(testutil.SampleClashSubscription), 0o600); err != nil {
		t.Fatal(err)
	}
	custom, err := ParseCustomRules("DOMAIN-SUFFIX,corp.example,DIRECT\nPROCESS-NAME,WeChat.exe,DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	for _, strict := range []bool{true, false} {
		text := y.Render(Build(Options{
			ControllerPort: 19097, ControllerSecret: "abcdef123456", TUN: true, StrictRoute: strict,
			DirectHosts: []string{"sub.example.com"}, CustomRules: custom,
			LocalDNS: []string{"192.168.1.1"}, TailscaleDNS: true, MixedPort: 17890,
		}))
		path := filepath.Join(home, "config.yaml")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(core, "-t", "-d", home, "-f", path).CombinedOutput()
		if err != nil {
			t.Fatalf("mihomo -t 失败（strict=%v）：%v\n%s\n配置：\n%s", strict, err, out, text)
		}
	}
	probe := y.Render(BuildProbe(19098, "abcdef"))
	path := filepath.Join(home, "probe.yaml")
	if err := os.WriteFile(path, []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(core, "-t", "-d", home, "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("探测配置没有通过校验：%v\n%s", err, out)
	}
}
