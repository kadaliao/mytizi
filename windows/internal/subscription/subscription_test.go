package subscription

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/testutil"
)

func TestNormalizesPastedLinks(t *testing.T) {
	cases := map[string]string{
		"  https://sub.example.com/api/v1/client/subscribe?token=abc \n":                  "https://sub.example.com/api/v1/client/subscribe?token=abc",
		"https://sub.example.com/x?token=\nabc":                                           "https://sub.example.com/x?token=abc",
		"\r\nhttps://sub.example.com/x?token=abc\r\n":                                     "https://sub.example.com/x?token=abc",
		"clash://install-config?url=https%3A%2F%2Fsub.example.com%2Fa%3Ftoken%3D1&name=x": "https://sub.example.com/a?token=1",
	}
	for input, want := range cases {
		u, err := NormalizeURL(input)
		if err != nil || u.String() != want {
			t.Errorf("NormalizeURL(%q) = %v, %v; want %s", input, u, err, want)
		}
	}
}

func TestRejectsNonSubscriptionInput(t *testing.T) {
	for _, bad := range []string{"", "hello", "ftp://example.com/a", "https://", "example.com/sub"} {
		if _, err := NormalizeURL(bad); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("NormalizeURL(%q) 应当失败，得到 %v", bad, err)
		}
	}
}

func TestParsesUserInfoHeader(t *testing.T) {
	usage := ParseUserInfo("upload=455727941; download=6174315083; total=1073741824000; expire=1671815872")
	if usage == nil || usage.Upload != 455727941 || usage.Download != 6174315083 || usage.Total != 1073741824000 {
		t.Fatalf("got %+v", usage)
	}
	if usage.Used() != 6630043024 || usage.Expire == nil || usage.Expire.Unix() != 1671815872 {
		t.Fatalf("got %+v", usage)
	}
	if ParseUserInfo("garbage") != nil {
		t.Error("无法解析的头应返回 nil")
	}
	if u := ParseUserInfo("download=1; total=2; expire=0"); u == nil || u.Expire != nil {
		t.Error("expire=0 表示不过期")
	}
}

func TestParsesTitle(t *testing.T) {
	cases := []struct{ title, disposition, want string }{
		{"base64:5oiR55qE6K6i6ZiF", "", "我的订阅"},
		{"Plain", "", "Plain"},
		{"", "attachment; filename*=UTF-8''%E8%8A%82%E7%82%B9.yaml", "节点"},
		{"", "attachment; filename=\"company.yaml\"", "company"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := ParseTitle(c.title, c.disposition); got != c.want {
			t.Errorf("ParseTitle(%q, %q) = %q, want %q", c.title, c.disposition, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	if !errors.Is(Validate(nil), ErrEmpty) {
		t.Error("空内容应报 ErrEmpty")
	}
	var e *Error
	if err := Validate([]byte("<!DOCTYPE html><html>login</html>")); !errors.As(err, &e) || e.Kind != "not-subscription" {
		t.Errorf("网页应被识别出来，得到 %v", err)
	}
	if err := Validate([]byte("proxies:\n  - {name: a}")); err != nil {
		t.Error(err)
	}
}

func TestFetchReadsBodyAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("UA = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("subscription-userinfo", "upload=1; download=2; total=10")
		w.Header().Set("profile-title", "base64:5YWs5Y+4")
		w.Header().Set("profile-update-interval", "12")
		_, _ = w.Write([]byte(testutil.SampleClashSubscription))
	}))
	defer server.Close()
	u, _ := NormalizeURL(server.URL + "/sub?token=abc")
	fetched, err := Fetch(context.Background(), u, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(fetched.Body) != testutil.SampleClashSubscription || fetched.Usage.Total != 10 ||
		fetched.Title != "公司" || fetched.UpdateIntervalHours != 12 {
		t.Fatalf("got %+v", fetched)
	}
}

func TestFetchReportsHTTPStatusAndHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/forbidden" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("<html><body>请登录</body></html>"))
	}))
	defer server.Close()
	u, _ := NormalizeURL(server.URL + "/forbidden")
	_, err := Fetch(context.Background(), u, 5*time.Second)
	var e *Error
	if !errors.As(err, &e) || e.Kind != "http" || e.Code != 403 {
		t.Fatalf("得到 %v", err)
	}
	u, _ = NormalizeURL(server.URL + "/page")
	if _, err := Fetch(context.Background(), u, 5*time.Second); !errors.As(err, &e) || e.Kind != "not-subscription" {
		t.Fatalf("得到 %v", err)
	}
}

func TestFetchReportsUnreachableServer(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	u, _ := NormalizeURL("http://" + addr + "/sub")
	_, err := Fetch(context.Background(), u, 3*time.Second)
	var e *Error
	if !errors.As(err, &e) || e.Kind != "unreachable" {
		t.Fatalf("得到 %v", err)
	}
}
