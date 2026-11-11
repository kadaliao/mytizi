// Package update 检查 GitHub Releases 上有没有新版本。只负责"发现有新版本"，
// 然后由 App 打开下载地址让用户手动安装：安装包没有代码签名，悄悄替换自己会被 SmartScreen 拦下。
//
// 用 /releases/latest 的重定向拿版本号（/releases/tag/vX.Y.Z），不调 GitHub API，
// 避免未登录调用每小时 60 次的限流。
package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

// Info 是一次"有新版本"的结论。
type Info struct {
	// Version 是去掉前导 v 的版本号，例如 0.4.0。
	Version string
	// Tag 是上游 tag，例如 v0.4.0。
	Tag        string
	ReleaseURL string
	// DownloadURL 是本机架构的安装包地址；找不到时为空，界面退回 ReleaseURL。
	DownloadURL string
}

// Error 是检查更新失败的原因。
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

var ErrNoRelease = &Error{"上游还没有发布版本。"}

func unreachable(reason string) error { return &Error{"检查更新失败：" + reason} }

// Version 是语义化版本（点分数字 + 可选 -预发布）。数字逐段比较，数字相同时正式版比预发布版新。
type Version struct {
	Numbers    []int
	Prerelease string
}

// ParseVersion 解析 v0.2.0、0.2.0-beta.1、0.2.0+build7 这类写法。
func ParseVersion(text string) (Version, bool) {
	value := strings.TrimSpace(text)
	if strings.HasPrefix(value, "v") || strings.HasPrefix(value, "V") {
		value = value[1:]
	}
	if value == "" {
		return Version{}, false
	}
	var v Version
	if dash := strings.Index(value, "-"); dash >= 0 {
		v.Prerelease = strings.ToLower(value[dash+1:])
		value = value[:dash]
	}
	for _, part := range strings.Split(value, ".") {
		// 允许 0.2.0+build 这种构建元数据，忽略 + 之后的内容。
		digits := strings.SplitN(part, "+", 2)[0]
		if digits == "" || strings.Trim(digits, "0123456789") != "" {
			return Version{}, false
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return Version{}, false
		}
		v.Numbers = append(v.Numbers, n)
	}
	return v, true
}

func (v Version) String() string {
	parts := make([]string, len(v.Numbers))
	for i, n := range v.Numbers {
		parts[i] = strconv.Itoa(n)
	}
	s := strings.Join(parts, ".")
	if v.Prerelease != "" {
		s += "-" + v.Prerelease
	}
	return s
}

// Less 报告 v 是否比 other 旧。
func (v Version) Less(other Version) bool {
	n := len(v.Numbers)
	if len(other.Numbers) > n {
		n = len(other.Numbers)
	}
	for i := 0; i < n; i++ {
		a, b := 0, 0
		if i < len(v.Numbers) {
			a = v.Numbers[i]
		}
		if i < len(other.Numbers) {
			b = other.Numbers[i]
		}
		if a != b {
			return a < b
		}
	}
	switch {
	case v.Prerelease == "" && other.Prerelease == "":
		return false
	case v.Prerelease == "":
		return false // 正式版比预发布版新
	case other.Prerelease == "":
		return true
	default:
		return v.Prerelease < other.Prerelease
	}
}

// IsNewer 报告 candidate 是否比 current 新；current 无法解析（例如 dev）时总是 false。
func IsNewer(candidate, current string) bool {
	c, ok1 := ParseVersion(candidate)
	cur, ok2 := ParseVersion(current)
	return ok1 && ok2 && cur.Less(c)
}

// NormalizedTag 去掉前导 v 与空白。
func NormalizedTag(tag string) string {
	value := strings.TrimSpace(tag)
	if strings.HasPrefix(value, "v") || strings.HasPrefix(value, "V") {
		value = value[1:]
	}
	return value
}

// Checker 访问 GitHub 网页（不是 API）。
type Checker struct {
	Repository string
	BaseURL    string
	Client     *http.Client
	// Arch 是安装包的架构后缀：x64 或 arm64。默认按当前程序的架构。
	Arch string
}

// NewChecker 返回检查本项目的 Checker：12 秒超时，不走系统代理。
func NewChecker() *Checker {
	return &Checker{
		Repository: appinfo.Repository,
		BaseURL:    "https://github.com",
		Client:     &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil}},
	}
}

var repoComponent = `[A-Za-z0-9_-][A-Za-z0-9_.-]*`
var repoPattern = regexp.MustCompile(`^` + repoComponent + `/` + repoComponent + `$`)
var safeTag = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// LatestURL 是 /releases/latest 的地址；仓库名不合法时为空。
func (c *Checker) LatestURL() string {
	if !repoPattern.MatchString(c.Repository) || strings.Contains(c.Repository, "..") {
		return ""
	}
	return strings.TrimRight(c.BaseURL, "/") + "/" + c.Repository + "/releases/latest"
}

func (c *Checker) arch() string {
	if c.Arch != "" {
		return c.Arch
	}
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x64"
}

func (c *Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

// Latest 读出最新正式版的版本号与下载地址。
func (c *Checker) Latest(ctx context.Context) (*Info, error) {
	latest := c.LatestURL()
	if latest == "" {
		return nil, unreachable("仓库地址无效")
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, unreachable("地址无效")
	}
	body, final, err := c.get(ctx, latest)
	if err != nil {
		return nil, err
	}
	// 只接受跳转到同一个站点：避免本机代理把"检查更新"导向任意页面/下载地址。
	if final.Host != base.Host || final.Scheme != base.Scheme {
		return nil, &Error{"检查更新失败：跳转到了意外的地址"}
	}
	// /releases/latest 会 302 到 /releases/tag/vX.Y.Z；没有正式版本时落到 /releases。
	components := strings.Split(strings.Trim(final.Path, "/"), "/")
	tag := ""
	for i, part := range components {
		if part == "tag" && i+1 < len(components) {
			tag, _ = url.PathUnescape(components[i+1])
			break
		}
	}
	if tag == "" {
		return nil, ErrNoRelease
	}
	version := NormalizedTag(tag)
	if _, ok := ParseVersion(version); !ok {
		return nil, &Error{"无法识别上游版本号：" + tag}
	}
	info := &Info{Version: version, Tag: tag, ReleaseURL: final.String()}
	// tag 会拼进下载 URL，必须是安全字符；否则只打开已经验证过的 tag 页面。
	if !safeTag.MatchString(tag) {
		return info, nil
	}
	info.ReleaseURL = strings.TrimRight(c.BaseURL, "/") + "/" + c.Repository + "/releases/tag/" + tag
	info.DownloadURL = c.installerURL(body, tag, base)
	if info.DownloadURL == "" {
		// GitHub 的发布页把资产列表放在 include-fragment 里延迟加载，单独取一次。
		fragment := strings.TrimRight(c.BaseURL, "/") + "/" + c.Repository + "/releases/expanded_assets/" + tag
		if more, finalFragment, err := c.get(ctx, fragment); err == nil && finalFragment.Host == base.Host {
			info.DownloadURL = c.installerURL(more, tag, base)
		}
	}
	return info, nil
}

func (c *Checker) get(ctx context.Context, address string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, nil, unreachable(err.Error())
	}
	req.Header.Set("User-Agent", appinfo.DisplayName+"/update-check")
	req.Header.Set("Accept", "text/html")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, nil, unreachable(describe(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, &Error{"检查更新失败：HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, unreachable(describe(err))
	}
	return body, resp.Request.URL, nil
}

var hrefPattern = regexp.MustCompile(`href="([^"]*?/releases/download/[^"]*?)"`)

// installerURL 在网页里找本机架构的安装包链接（MyTizi-<版本>-windows-<架构>-setup.exe）。
func (c *Checker) installerURL(html []byte, tag string, base *url.URL) string {
	suffix := "-windows-" + c.arch() + "-setup.exe"
	for _, m := range hrefPattern.FindAllSubmatch(html, -1) {
		link := strings.ReplaceAll(string(m[1]), "&amp;", "&")
		if !strings.Contains(link, "/releases/download/"+tag+"/") || !strings.HasSuffix(strings.ToLower(link), suffix) {
			continue
		}
		ref, err := url.Parse(link)
		if err != nil {
			continue
		}
		resolved := base.ResolveReference(ref)
		if resolved.Host != base.Host && !strings.HasSuffix(resolved.Host, ".githubusercontent.com") {
			continue
		}
		return resolved.String()
	}
	return ""
}

func describe(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "连接超时"
		}
		return urlErr.Err.Error()
	}
	return err.Error()
}
