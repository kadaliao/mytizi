// Package redact 是诊断报告与日志的脱敏。原则：报告要能直接发给 AI / 同事，
// 任何能"登录"或"使用"你的账号/节点的东西都不能出现。
//
// 两层防护：
//  1. 精确替换：已知的秘密（订阅链接、控制密钥、订阅 token）按原文替换；
//  2. 模式替换：即使没登记，常见形态（token=…、Authorization: Bearer …、UUID、password=…）也会被抹掉。
//
// 不脱敏的东西（域名、IP、节点名）是排查所必需的，报告头部会明确告知。
package redact

import (
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type secret struct {
	value string
	label string
}

type Redactor struct {
	secrets []secret
	home    string
}

// New 创建脱敏器。homeDir 为空时使用当前用户的家目录。
func New(homeDir string) *Redactor {
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	return &Redactor{home: homeDir}
}

// Add 登记一个必须抹掉的原文。过短的值（<6）会误伤普通文本，直接忽略。
func (r *Redactor) Add(value, label string) {
	if utf8.RuneCountInString(value) < 6 {
		return
	}
	r.secrets = append(r.secrets, secret{value, label})
}

// AddSubscriptionURL 登记订阅链接：整条链接、query 里的每个值、以及路径里的长 token。
func (r *Redactor) AddSubscriptionURL(raw string) {
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	r.Add(raw, "订阅链接")
	for _, values := range u.Query() {
		for _, v := range values {
			r.Add(v, "订阅参数")
		}
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if utf8.RuneCountInString(segment) >= 16 {
			r.Add(segment, "订阅令牌")
		}
	}
}

var patterns = []struct {
	re       *regexp.Regexp
	template string
}{
	// 带 token 的 URL 参数
	{regexp.MustCompile(`(?i)([?&](?:token|key|secret|password|passwd|pwd|auth|sign|signature|access_token|api_key|apikey|uuid)=)[^&\s"']+`), "${1}<已脱敏>"},
	{regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+)[^\s"']+`), "${1}<已脱敏>"},
	{regexp.MustCompile(`(?i)("?(?:password|passwd|secret|psk|private-key|uuid)"?\s*[:=]\s*"?)[^\s",}]+`), "${1}<已脱敏>"},
	// 协议链接里的凭据：ss://xxxx@host、trojan://pass@host、vmess://base64…
	{regexp.MustCompile(`(?i)\b(ss|ssr|trojan|vless|hysteria2?|tuic|vmess|anytls)://[^\s"'<>]+`), "${1}://<已脱敏>"},
	// 裸 UUID（vmess/vless 用户 ID）
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
}

var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// Redact 返回脱敏后的文本。
func (r *Redactor) Redact(text string) string {
	result := text
	// 长的先换，避免短秘密是长秘密的子串时留下残片。
	ordered := append([]secret(nil), r.secrets...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].value) > len(ordered[j].value) })
	for _, s := range ordered {
		result = strings.ReplaceAll(result, s.value, "<已脱敏:"+s.label+">")
	}
	for _, p := range patterns {
		result = p.re.ReplaceAllString(result, p.template)
	}
	return r.replaceHome(result)
}

// replaceHome 把家目录换成 ~。Windows 路径不区分大小写，而且同一个目录会以
// C:\Users\alice、C:/Users/alice、C:\\Users\\alice（JSON 转义）几种形态出现。
func (r *Redactor) replaceHome(text string) string {
	home := strings.TrimRight(r.home, `\/`)
	if len(home) <= 1 {
		return text
	}
	if !windowsPath.MatchString(home + `\`) {
		return strings.ReplaceAll(text, home, "~")
	}
	parts := strings.FieldsFunc(home, func(c rune) bool { return c == '\\' || c == '/' })
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := regexp.MustCompile(`(?i)` + strings.Join(parts, `(?:\\\\|\\|/)`))
	return re.ReplaceAllString(text, "~")
}
