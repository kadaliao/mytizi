// Package subscription 下载订阅并读出服务端附带的信息（流量、标题、刷新间隔）。
// 订阅内容本身不在这里解析：交给 mihomo（见 probe 包），和真正连接时用同一个解析器。
package subscription

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/settings"
)

// UserAgent：订阅面板通常按 UA 决定返回 Clash YAML 还是通用 base64；带上 clash.meta / mihomo 关键字拿到的格式最全。
const UserAgent = "clash.meta/1.19 mihomo/1.19 MyTizi/0.1"

// MaxBodyBytes 是订阅内容的上限。
const MaxBodyBytes = 20 * 1024 * 1024

// Error 是给用户看的订阅错误。
type Error struct {
	Kind    string // invalid-url, unreachable, http, empty, not-subscription, no-nodes
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

var (
	ErrInvalidURL = &Error{Kind: "invalid-url", Message: "这不是有效的订阅链接，请粘贴以 http:// 或 https:// 开头的完整地址。"}
	ErrEmpty      = &Error{Kind: "empty", Message: "订阅内容是空的。"}
	ErrNoNodes    = &Error{Kind: "no-nodes", Message: "订阅里没有找到可用的线路。"}
)

func unreachable(reason string) *Error {
	return &Error{Kind: "unreachable", Message: "连不上订阅服务器：" + reason}
}

func httpError(code int) *Error {
	var message string
	switch code {
	case 401, 403:
		message = fmt.Sprintf("订阅服务器拒绝访问（%d），链接可能已失效或被停用。", code)
	case 404:
		message = "订阅地址不存在（404），请检查链接是否复制完整。"
	default:
		message = fmt.Sprintf("订阅服务器返回了错误（HTTP %d）。", code)
	}
	return &Error{Kind: "http", Code: code, Message: message}
}

// NotASubscription 包装"内容不像订阅"的原因。
func NotASubscription(hint string) *Error { return &Error{Kind: "not-subscription", Message: hint} }

// Fetched 是一次下载的结果。
type Fetched struct {
	Body                []byte
	Usage               *settings.Usage
	Title               string
	UpdateIntervalHours int
}

// NormalizeURL 接受用户粘贴的各种形态：首尾空白、换行、以及 clash:// 之类的导入链接。
func NormalizeURL(input string) (*url.URL, error) {
	text := strings.TrimSpace(input)
	text = strings.NewReplacer("\n", "", "\r", "").Replace(text)
	if u, err := url.Parse(text); err == nil && u.Scheme != "" {
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			// clash://install-config?url=https%3A%2F%2F… 这类一键导入链接
			if inner := u.Query().Get("url"); inner != "" {
				text = inner
			}
		}
	}
	u, err := url.Parse(text)
	if err != nil {
		return nil, ErrInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" {
		return nil, ErrInvalidURL
	}
	return u, nil
}

// Fetch 直连下载订阅：不走任何代理（订阅主机在规则里固定直连，节点全挂时也能刷新）。
func Fetch(ctx context.Context, u *url.URL, timeout time.Duration) (*Fetched, error) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{
		Timeout:   timeout * 2,
		Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrInvalidURL
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, unreachable(Describe(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, unreachable(Describe(err))
	}
	if err := Validate(body); err != nil {
		return nil, err
	}
	interval, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("profile-update-interval")))
	return &Fetched{
		Body:                body,
		Usage:               ParseUserInfo(resp.Header.Get("subscription-userinfo")),
		Title:               ParseTitle(resp.Header.Get("profile-title"), resp.Header.Get("content-disposition")),
		UpdateIntervalHours: interval,
	}, nil
}

// Validate 挡掉空内容、超大内容和网页。
func Validate(body []byte) error {
	if len(body) == 0 {
		return ErrEmpty
	}
	if len(body) > MaxBodyBytes {
		return NotASubscription("订阅内容过大，不像是订阅文件。")
	}
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(head), "\ufeff")))
	for _, prefix := range []string{"<!doctype html", "<html", "<?xml", "<head"} {
		if strings.HasPrefix(h, prefix) {
			return NotASubscription("订阅地址返回的是一个网页，不是订阅内容。链接可能已过期，或需要先登录。")
		}
	}
	return nil
}

// ParseUserInfo 解析 `upload=455727941; download=6174315083; total=1073741824000; expire=1671815872`。
func ParseUserInfo(header string) *settings.Usage {
	values := map[string]int64{}
	for _, part := range strings.Split(header, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		number, err := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64)
		if err != nil {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(kv[0]))] = number
	}
	_, hasTotal := values["total"]
	_, hasDown := values["download"]
	_, hasUp := values["upload"]
	if !hasTotal && !hasDown && !hasUp {
		return nil
	}
	usage := &settings.Usage{Upload: values["upload"], Download: values["download"], Total: values["total"]}
	if expire := values["expire"]; expire > 0 {
		t := time.Unix(expire, 0)
		usage.Expire = &t
	}
	return usage
}

// ParseTitle：profile-title 可能是 `base64:xxxx`；否则取 content-disposition 里的文件名。
func ParseTitle(profileTitle, disposition string) string {
	if title := strings.TrimSpace(profileTitle); title != "" {
		if strings.HasPrefix(strings.ToLower(title), "base64:") {
			if decoded, err := base64.StdEncoding.DecodeString(title[7:]); err == nil {
				return string(decoded)
			}
		}
		return title
	}
	if disposition == "" {
		return ""
	}
	lower := strings.ToLower(disposition)
	// filename*=UTF-8''%E8%8A%82%E7%82%B9.yaml 优先于 filename="x.yaml"
	for _, key := range []string{"filename*=utf-8''", "filename=\""} {
		i := strings.Index(lower, key)
		if i < 0 {
			continue
		}
		rest := disposition[i+len(key):]
		if end := strings.IndexAny(rest, "\";"); end >= 0 {
			rest = rest[:end]
		}
		if decoded, err := url.PathUnescape(rest); err == nil {
			rest = decoded
		}
		name := strings.TrimSuffix(rest, path.Ext(rest))
		if name != "" {
			return name
		}
	}
	return ""
}

// Describe 把网络错误翻成人话。
func Describe(err error) string {
	var dnsErr *net.DNSError
	var certErr *x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var netErr net.Error
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return "找不到服务器，域名解析失败。"
	case errors.As(err, &certErr), errors.As(err, &hostErr), errors.As(err, &invalidCert):
		return "安全连接失败（证书问题）。"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "连接超时，请检查网络。"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "服务器拒绝连接。"
	}
	return err.Error()
}
