package diag

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Route 是一条连接的去向。
type Route string

const (
	RouteDirect Route = "直连"
	RouteProxy  Route = "代理"
	RouteReject Route = "拒绝"
)

// SelfTestResult 是一项实机自检的结果。
type SelfTestResult struct {
	Name     string
	URL      string
	Expected Route // 空表示不判断走向
	// Reachable：这一项探测得到了结果（HTTP 有响应、DNS 有解析）。
	Reachable    bool
	Status       int
	Milliseconds int
	Error        string
	// Observed 是从核心日志里读到的实际命中规则与出口，例如 "DomainSuffix(baidu.com) → DIRECT"。
	Observed      string
	ObservedRoute Route
	// Concern 是需要人注意但不属于"不通"的情况（比如 DNS 没有被接管、IPv6 没被拦）。
	Concern string
}

// RouteMismatch：期望的出口和实际出口不一致（两者都已知才判断）。
func (r SelfTestResult) RouteMismatch() bool {
	return r.Expected != "" && r.ObservedRoute != "" && r.Expected != r.ObservedRoute
}

func (r SelfTestResult) Passed() bool { return r.Reachable && !r.RouteMismatch() && r.Concern == "" }

// RouteLogEntry 是核心日志里一条连接记录：
// `[TCP] 198.18.0.1:60822 --> www.baidu.com:443 match DomainSuffix(baidu.com) using DIRECT`
type RouteLogEntry struct {
	Host  string
	Port  int
	Rule  string
	Chain string
}

func (e RouteLogEntry) Route() Route {
	switch {
	case strings.HasPrefix(e.Chain, "DIRECT"):
		return RouteDirect
	case strings.HasPrefix(e.Chain, "REJECT"):
		return RouteReject
	default:
		return RouteProxy
	}
}

var routeLine = regexp.MustCompile(`-->\s+(\S+?):(\d+)\s+match\s+(.+?)\s+using\s+(.+?)"?\s*$`)

// ParseRouteLogLine 解析一行核心日志；不是连接记录时返回 false。
func ParseRouteLogLine(line string) (RouteLogEntry, bool) {
	m := routeLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if m == nil {
		return RouteLogEntry{}, false
	}
	port, err := strconv.Atoi(m[2])
	if err != nil {
		return RouteLogEntry{}, false
	}
	return RouteLogEntry{Host: m[1], Port: port, Rule: m[3], Chain: m[4]}, true
}

// LatestRouteFor 在日志文本里找某个主机最近的一条记录。
func LatestRouteFor(host, log string) (RouteLogEntry, bool) {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], "--> "+host+":") {
			continue
		}
		if entry, ok := ParseRouteLogLine(lines[i]); ok && entry.Host == host {
			return entry, true
		}
	}
	return RouteLogEntry{}, false
}

// Probe 是一个自检目标。
type Probe struct {
	Name     string
	URL      string
	Expected Route
}

// Probes 与 macOS 版相同。
var Probes = []Probe{
	{"国内网站", "https://www.baidu.com/", RouteDirect},
	// 腾讯的根域名（qpic.cn）不会返回内容，换成一个真实的静态图片，避免把"CDN 正常"误报成超时。
	{"国内 CDN（腾讯图片）", "https://thirdqq.qlogo.cn/g?b=sdk&nk=10000&s=100", RouteDirect},
	{"国内 CDN（阿里）", "https://g.alicdn.com/", RouteDirect},
	{"海外（gstatic 204）", "https://www.gstatic.com/generate_204", RouteProxy},
	{"海外（google）", "https://www.google.com/generate_204", RouteProxy},
}

// IPv6Target 是 IPv6 拦截检查的目标（阿里公共 DNS 的 IPv6 地址，443 端口）。
const IPv6Target = "[2400:3200::1]:443"

// RunSelfTest 经由真实网络栈访问几个有代表性的地址，并对照核心日志确认实际走了哪条路。
// checkIPv6 为 true 时额外确认 IPv6 出站被拦住了（本机有公网 IPv6 地址时才有意义）。
func RunSelfTest(ctx context.Context, coreLog func() string, checkIPv6 bool) []SelfTestResult {
	ordered := make([]SelfTestResult, len(Probes))
	var wg sync.WaitGroup
	// 五个探测并发跑：节点全挂时最坏耗时从 5×8 秒降到约 8 秒。
	for i, p := range Probes {
		wg.Add(1)
		go func(i int, p Probe) {
			defer wg.Done()
			ordered[i] = runProbe(ctx, p, coreLog)
		}(i, p)
	}
	var v6 *SelfTestResult
	if checkIPv6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := ipv6Check(ctx)
			v6 = &r
		}()
	}
	dns := dnsCheck(ctx)
	wg.Wait()
	out := append([]SelfTestResult{dns}, ordered...)
	if v6 != nil {
		out = append(out, *v6)
	}
	return out
}

func runProbe(ctx context.Context, p Probe, coreLog func() string) SelfTestResult {
	result := SelfTestResult{Name: p.Name, URL: p.URL, Expected: p.Expected}
	// 绕开系统代理设置，测的是 TUN 本身。
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err == nil {
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			result.Reachable = true
			result.Status = resp.StatusCode
			resp.Body.Close()
		}
	}
	if err != nil {
		result.Error = shortError(err)
	}
	result.Milliseconds = int(time.Since(started).Milliseconds())

	// 日志是异步落盘的，稍等一下再读。
	time.Sleep(400 * time.Millisecond)
	if u, err := url.Parse(p.URL); err == nil {
		if entry, ok := LatestRouteFor(u.Hostname(), coreLog()); ok {
			result.Observed = entry.Rule + " → " + entry.Chain
			result.ObservedRoute = entry.Route()
		}
	}
	return result
}

// dnsCheck 解析一个国内域名：TUN 接管 DNS 成功时应当拿到 198.18.0.0/16 的 fake-ip。
func dnsCheck(ctx context.Context) SelfTestResult {
	result := SelfTestResult{Name: "DNS 是否被接管", URL: "系统解析 www.baidu.com"}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started := time.Now()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", "www.baidu.com")
	result.Milliseconds = int(time.Since(started).Milliseconds())
	if err != nil || len(ips) == 0 {
		if err != nil {
			result.Error = shortError(err)
		} else {
			result.Error = "没有解析结果"
		}
		return result
	}
	ip := ips[0].String()
	result.Reachable = true
	if strings.HasPrefix(ip, "198.18.") {
		result.Observed = ip + "（fake-ip，已被接管）"
	} else {
		result.Observed = ip + "（真实地址）"
		result.Concern = "解析到的是真实地址而不是 fake-ip：系统 DNS 没有经过 TUN（未连接时这是正常的）。"
	}
	return result
}

// ipv6Check：连接期间 strict-route 应当拦掉所有 IPv6 出站；连得上说明 IPv6 流量会绕过规则。
func ipv6Check(ctx context.Context) SelfTestResult {
	result := SelfTestResult{Name: "IPv6 是否被拦截", URL: "tcp " + IPv6Target, Reachable: true}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	started := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp6", IPv6Target)
	result.Milliseconds = int(time.Since(started).Milliseconds())
	if err != nil {
		result.Observed = "已拦截（" + shortError(err) + "）"
		return result
	}
	conn.Close()
	result.Observed = "IPv6 连接成功"
	result.Concern = "IPv6 连接没有被拦截：应用拿到 IPv6 地址时会直接绕过 TUN 和规则。检查高级设置里的「严格接管」是否开启。"
	return result
}

func shortError(err error) string {
	text := err.Error()
	if len(text) > 160 {
		text = text[:160] + "…"
	}
	return text
}

func (r SelfTestResult) String() string {
	return fmt.Sprintf("%s %s reachable=%v observed=%s", r.Name, r.URL, r.Reachable, r.Observed)
}
