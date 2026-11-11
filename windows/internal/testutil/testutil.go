// Package testutil 给测试找内置 mihomo 与地理库（scripts/fetch-core.sh 下载到仓库根目录的 Vendor/core）。
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Root 是仓库根目录（windows/ 的上一级）。
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func VendorDir() string { return filepath.Join(Root(), "Vendor", "core") }

// CoreBinary 返回当前平台与架构对应的 mihomo；没有就返回 ""。
func CoreBinary() string {
	var name string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		name = "mihomo-arm64"
	case "darwin/amd64":
		name = "mihomo-amd64"
	case "windows/amd64":
		name = "mihomo-windows-amd64.exe"
	case "windows/arm64":
		name = "mihomo-windows-arm64.exe"
	default:
		return ""
	}
	path := filepath.Join(VendorDir(), name)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// RequireCore 在缺少核心或地理库时跳过测试。
func RequireCore(t testing.TB) string {
	t.Helper()
	core := CoreBinary()
	if core == "" {
		t.Skip("缺少 Vendor/core 里的 mihomo，先运行 scripts/fetch-core.sh（Windows：windows/scripts/fetch-core.sh）")
	}
	for _, name := range []string{"geoip.metadb", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(VendorDir(), name)); err != nil {
			t.Skip("缺少地理库 " + name)
		}
	}
	return core
}

// CoreHome 准备一个带地理库的 mihomo 工作目录。
func CoreHome(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"geoip.metadb", "geosite.dat"} {
		data, err := os.ReadFile(filepath.Join(VendorDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// SampleClashSubscription 是一份最小的 Clash 订阅（服务器是文档保留地址，不会真的连）。
const SampleClashSubscription = `proxies:
  - {name: "节点-香港", type: ss, server: 203.0.113.10, port: 8388, cipher: aes-128-gcm, password: "secret-pass"}
  - {name: "节点-网关", type: http, server: 192.0.2.5, port: 7890}
`
