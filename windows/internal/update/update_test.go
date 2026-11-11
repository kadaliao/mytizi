package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParsesCommonTagShapes(t *testing.T) {
	cases := map[string]string{"v0.1.0": "0.1.0", " 1.10.2 ": "1.10.2", "0.2.0-beta.1": "0.2.0-beta.1", "0.2.0+build7": "0.2.0", "0.2": "0.2"}
	for input, want := range cases {
		v, ok := ParseVersion(input)
		if !ok || v.String() != want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %s", input, v, ok, want)
		}
	}
	if v, _ := ParseVersion("0.2.0-beta.1"); v.Prerelease != "beta.1" {
		t.Error("预发布标记没有解析出来")
	}
	for _, bad := range []string{"dev", "v", "1.x.0", ""} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q 不应被解析", bad)
		}
	}
}

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, ok := ParseVersion(s)
	if !ok {
		t.Fatalf("无法解析 %q", s)
	}
	return v
}

func TestComparesNumericallyNotLexically(t *testing.T) {
	if !mustParse(t, "0.9.0").Less(mustParse(t, "0.10.0")) {
		t.Error("0.9.0 < 0.10.0")
	}
	if !mustParse(t, "1.0.0-beta").Less(mustParse(t, "1.0.0")) {
		t.Error("预发布版比正式版旧")
	}
	if !mustParse(t, "1.0.0-beta.2").Less(mustParse(t, "1.0.1")) {
		t.Error("1.0.0-beta.2 < 1.0.1")
	}
	if mustParse(t, "1.0.0").Less(mustParse(t, "1.0")) {
		t.Error("1.0.0 与 1.0 相等")
	}
}

func TestIsNewerIgnoresUnparseableCurrentVersion(t *testing.T) {
	if !IsNewer("0.2.0", "0.1.0") || IsNewer("0.1.0", "0.1.0") || IsNewer("0.1.0", "dev") {
		t.Error("IsNewer 判断错误")
	}
}

func TestNormalizesGitHubTags(t *testing.T) {
	for input, want := range map[string]string{"v0.2.0": "0.2.0", "V1.0": "1.0", " 0.3.0 ": "0.3.0"} {
		if got := NormalizedTag(input); got != want {
			t.Errorf("NormalizedTag(%q) = %q", input, got)
		}
	}
}

// fakeGitHub：/releases/latest 302 到 tag 页面；资产列表可选地放在 expanded_assets 片段里。
func fakeGitHub(t *testing.T, tagPage, fragment string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			http.Redirect(w, r, "/o/r/releases/tag/v0.4.0", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/releases/tag/v0.4.0"):
			_, _ = w.Write([]byte(tagPage))
		case strings.HasSuffix(r.URL.Path, "/releases/expanded_assets/v0.4.0"):
			_, _ = w.Write([]byte(fragment))
		default:
			http.NotFound(w, r)
		}
	}))
}

const assets = `<a href="/o/r/releases/download/v0.4.0/MyTizi-0.4.0.dmg">mac</a>
<a href="/o/r/releases/download/v0.4.0/MyTizi-0.4.0-windows-arm64-setup.exe">arm</a>
<a href="/o/r/releases/download/v0.4.0/MyTizi-0.4.0-windows-x64-setup.exe">x64</a>`

func TestLatestReadsVersionFromRedirect(t *testing.T) {
	server := fakeGitHub(t, "<html>"+assets+"</html>", "")
	defer server.Close()
	checker := &Checker{Repository: "o/r", BaseURL: server.URL, Client: server.Client(), Arch: "x64"}
	info, err := checker.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Tag != "v0.4.0" || info.Version != "0.4.0" {
		t.Fatalf("got %+v", info)
	}
	if info.ReleaseURL != server.URL+"/o/r/releases/tag/v0.4.0" {
		t.Errorf("ReleaseURL = %s", info.ReleaseURL)
	}
	if info.DownloadURL != server.URL+"/o/r/releases/download/v0.4.0/MyTizi-0.4.0-windows-x64-setup.exe" {
		t.Errorf("DownloadURL = %s", info.DownloadURL)
	}
	checker.Arch = "arm64"
	info, _ = checker.Latest(context.Background())
	if !strings.HasSuffix(info.DownloadURL, "windows-arm64-setup.exe") {
		t.Errorf("arm64 应选 arm64 安装包：%s", info.DownloadURL)
	}
	if !IsNewer(info.Version, "0.3.0") {
		t.Error("0.4.0 比 0.3.0 新")
	}
}

func TestDownloadLinkFromLazyLoadedFragment(t *testing.T) {
	server := fakeGitHub(t, `<include-fragment src="/o/r/releases/expanded_assets/v0.4.0"></include-fragment>`, assets)
	defer server.Close()
	checker := &Checker{Repository: "o/r", BaseURL: server.URL, Client: server.Client(), Arch: "x64"}
	info, err := checker.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(info.DownloadURL, "MyTizi-0.4.0-windows-x64-setup.exe") {
		t.Errorf("应从资产片段里找到安装包：%q", info.DownloadURL)
	}
}

func TestMissingInstallerLeavesDownloadEmpty(t *testing.T) {
	server := fakeGitHub(t, `<a href="/o/r/releases/download/v0.4.0/MyTizi-0.4.0.dmg">mac</a>`, "")
	defer server.Close()
	checker := &Checker{Repository: "o/r", BaseURL: server.URL, Client: server.Client(), Arch: "x64"}
	info, err := checker.Latest(context.Background())
	if err != nil || info.DownloadURL != "" {
		t.Fatalf("got %+v, %v", info, err)
	}
}

func TestNoReleaseAndRedirectToForeignHost(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("evil"))
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/none/r/releases/latest":
			http.Redirect(w, r, "/none/r/releases", http.StatusFound)
		case "/none/r/releases":
			_, _ = w.Write([]byte("no releases"))
		case "/evil/r/releases/latest":
			http.Redirect(w, r, foreign.URL+"/evil/r/releases/tag/v9.9.9", http.StatusFound)
		}
	}))
	defer server.Close()
	_, err := (&Checker{Repository: "none/r", BaseURL: server.URL, Client: server.Client()}).Latest(context.Background())
	if err != ErrNoRelease {
		t.Errorf("没有正式版应报 ErrNoRelease，得到 %v", err)
	}
	_, err = (&Checker{Repository: "evil/r", BaseURL: server.URL, Client: server.Client()}).Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "意外的地址") {
		t.Errorf("跳到别的站点应被拒绝，得到 %v", err)
	}
}

func TestRejectsRepositoryNamesThatCouldEscapeThePath(t *testing.T) {
	for repo, ok := range map[string]bool{"../evil": false, "a/b/c": false, "kadaliao/mytizi": true} {
		if got := (&Checker{Repository: repo, BaseURL: "https://github.com"}).LatestURL() != ""; got != ok {
			t.Errorf("LatestURL(%q) 有效 = %v", repo, got)
		}
	}
}
