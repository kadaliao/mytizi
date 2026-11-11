package rules

import (
	"regexp"
	"strings"
	"testing"
)

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func TestDomainsAreWellFormedAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range append(append(DirectDomains(), ProxyDomains()...), WindowsDirect...) {
		if !domainPattern.MatchString(d) {
			t.Errorf("域名格式不对：%q", d)
		}
		if seen[d] {
			t.Errorf("重复的域名：%q", d)
		}
		seen[d] = true
	}
}

// 子域名被父域名覆盖就是冗余；直连与代理互相遮蔽则是错误。
func TestNoShadowing(t *testing.T) {
	all := append(append(DirectDomains(), ProxyDomains()...), WindowsDirect...)
	for _, a := range all {
		for _, b := range all {
			if a != b && strings.HasSuffix(a, "."+b) {
				t.Errorf("%q 已被 %q 覆盖", a, b)
			}
		}
	}
}

func TestKeyDomainsPresent(t *testing.T) {
	direct := strings.Join(DirectDomains(), " ")
	for _, d := range []string{"qq.com", "qpic.cn", "feishu.cn", "feishucdn.com", "byteimg.com", "alicdn.com"} {
		if !strings.Contains(" "+direct+" ", " "+d+" ") {
			t.Errorf("直连清单缺少 %s", d)
		}
	}
	proxy := strings.Join(ProxyDomains(), " ")
	for _, d := range []string{"youtube.com", "googlevideo.com", "claude.ai", "anthropic.com", "googleusercontent.com"} {
		if !strings.Contains(" "+proxy+" ", " "+d+" ") {
			t.Errorf("代理清单缺少 %s", d)
		}
	}
}
