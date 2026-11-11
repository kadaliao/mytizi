package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 与 macOS 版的 RuleCatalog.swift 逐项对比：两个平台的分流结果必须一致。
// 仓库外单独构建（没有 Swift 源码）时跳过。

func swiftCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "Sources", "MytiziCore", "RuleCatalog.swift")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("找不到 macOS 版规则清单（%v），跳过对比", err)
	}
	return string(data)
}

var quoted = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// staticList 取出 `static let NAME: ... = [ ... ]` 里的全部字符串。
func staticList(t *testing.T, source, name string) []string {
	t.Helper()
	start := strings.Index(source, "static let "+name)
	if start < 0 {
		t.Fatalf("Swift 源码里没有 %s", name)
	}
	body := source[start:]
	open := strings.Index(body, "= [")
	if open < 0 {
		t.Fatalf("%s 不是数组字面量", name)
	}
	body = body[open+2:]
	depth := 0
	end := -1
	inString := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' && (i == 0 || body[i-1] != '\\') {
			inString = !inString
		}
		if inString {
			continue
		}
		if c == '[' {
			depth++
		} else if c == ']' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end < 0 {
		t.Fatalf("%s 的数组没有闭合", name)
	}
	return withoutComments(body[:end+1])
}

// withoutComments 去掉 // 注释后取出字符串字面量。
func withoutComments(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && !strings.Contains(line[:i], `"`) {
			line = line[:i]
		}
		for _, m := range quoted.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

var groupPattern = regexp.MustCompile(`(?s)Group\(name: "([^"]+)", domains: \[(.*?)\]\)`)

func swiftGroups(t *testing.T, source, name string) []Group {
	t.Helper()
	start := strings.Index(source, "static let "+name)
	if start < 0 {
		t.Fatalf("Swift 源码里没有 %s", name)
	}
	rest := source[start:]
	// 到下一个 static 声明为止。
	if next := strings.Index(rest[1:], "public static "); next >= 0 {
		rest = rest[:next+1]
	}
	var groups []Group
	for _, m := range groupPattern.FindAllStringSubmatch(rest, -1) {
		groups = append(groups, Group{Name: m[1], Domains: withoutComments(m[2])})
	}
	if len(groups) == 0 {
		t.Fatalf("没有从 %s 里解析出任何分组", name)
	}
	return groups
}

func TestCatalogMatchesMacOS(t *testing.T) {
	source := swiftCatalog(t)
	cases := []struct {
		name string
		got  []string
	}{
		{"privateIPv4", PrivateIPv4},
		{"tunRouteExclude", TunRouteExclude},
		{"fakeIPFilter", FakeIPFilter},
		{"snifferSkip", SnifferSkip},
		{"domesticDNS", DomesticDNS},
	}
	for _, c := range cases {
		want := staticList(t, source, c.name)
		if !reflect.DeepEqual(c.got, want) {
			t.Errorf("%s 与 macOS 版不一致：\n  Windows %q\n  macOS   %q", c.name, c.got, want)
		}
	}
	for _, c := range []struct {
		name string
		got  []Group
	}{{"directGroups", DirectGroups}, {"proxyGroups", ProxyGroups}} {
		want := swiftGroups(t, source, c.name)
		if !reflect.DeepEqual(c.got, want) {
			t.Errorf("%s 与 macOS 版不一致：\n  Windows %v\n  macOS   %v", c.name, c.got, want)
		}
	}
}
