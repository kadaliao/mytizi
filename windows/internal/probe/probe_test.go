package probe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/testutil"
)

func names(t *testing.T, body string) ([]string, []string) {
	t.Helper()
	core := testutil.RequireCore(t)
	nodes, err := Nodes(context.Background(), []byte(body), core, t.TempDir(), 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var n, types []string
	for _, node := range nodes {
		n = append(n, node.Name)
		types = append(types, node.Type)
	}
	return n, types
}

func TestClashYAMLSubscription(t *testing.T) {
	n, types := names(t, testutil.SampleClashSubscription)
	if strings.Join(n, ",") != "节点-香港,节点-网关" || strings.Join(types, ",") != "Shadowsocks,Http" {
		t.Fatalf("got %v %v", n, types)
	}
}

// 通用订阅：整段 base64，里面是一行一个的 ss:// vmess:// trojan:// 链接。
func TestBase64URIListSubscription(t *testing.T) {
	vmess, _ := json.Marshal(map[string]string{
		"v": "2", "ps": "JP-03", "add": "203.0.113.3", "port": "443",
		"id": "b831381d-6324-4d53-ad4f-8cda48b30811", "aid": "0", "net": "ws", "type": "none",
		"host": "example.com", "path": "/ws", "tls": "tls",
	})
	list := strings.Join([]string{
		"ss://YWVzLTEyOC1nY206dGVzdHBhc3M@203.0.113.1:8388#HK-01",
		"trojan://pass123@203.0.113.2:443?sni=example.com#US-02",
		"vmess://" + base64.StdEncoding.EncodeToString(vmess),
	}, "\n")
	n, types := names(t, base64.StdEncoding.EncodeToString([]byte(list)))
	sort.Strings(n)
	sort.Strings(types)
	if strings.Join(n, ",") != "HK-01,JP-03,US-02" || strings.Join(types, ",") != "Shadowsocks,Trojan,Vmess" {
		t.Fatalf("got %v %v", n, types)
	}
}

func TestGarbageYieldsNoNodes(t *testing.T) {
	n, _ := names(t, "this is not a subscription")
	if len(n) != 0 {
		t.Fatalf("got %v", n)
	}
}

func TestMissingCore(t *testing.T) {
	_, err := Nodes(context.Background(), []byte("x"), "/nonexistent/mihomo", t.TempDir(), time.Second)
	if err != ErrCoreMissing {
		t.Fatalf("got %v", err)
	}
}
