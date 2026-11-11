// fetchcore 下载并校验 Windows 版内置的 mihomo 与地理数据库，放到仓库根目录的 Vendor/core/。
//
//	go run ./tools/fetchcore              按 ../core.lock 下载（已有且校验通过则跳过）
//	go run ./tools/fetchcore -refresh-geo 强制重新下载 GeoIP/GeoSite
//
// 用 Go 写而不是 shell：macOS、Linux 和 Windows 的 CI 上行为一致，不依赖 unzip/shasum/python。
// 与 macOS 版的 scripts/fetch-core.sh 共用同一个 core.lock 和同一份地理库。
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 5 * time.Minute}

func main() {
	refreshGeo := flag.Bool("refresh-geo", false, "强制重新下载地理库")
	flag.Parse()
	if err := run(*refreshGeo); err != nil {
		fmt.Fprintln(os.Stderr, "失败：", err)
		os.Exit(1)
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func run(refreshGeo bool) error {
	root := repoRoot()
	out := filepath.Join(root, "Vendor", "core")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	lock, err := readLock(filepath.Join(root, "core.lock"))
	if err != nil {
		return err
	}
	version := lock["MIHOMO_VERSION"]
	for _, arch := range []string{"amd64", "arm64"} {
		key := "MIHOMO_WINDOWS_" + strings.ToUpper(arch)
		asset, want := lock[key+"_ASSET"], lock[key+"_SHA256"]
		if asset == "" || want == "" {
			return fmt.Errorf("core.lock 里缺少 %s_ASSET / %s_SHA256", key, key)
		}
		if err := fetchCore(out, arch, version, asset, want); err != nil {
			return err
		}
	}
	if err := fetchLicense(out, version, lock["MIHOMO_LICENSE_SHA256"]); err != nil {
		return err
	}
	if err := fetchGeo(out, refreshGeo); err != nil {
		return err
	}
	published, _ := os.ReadFile(filepath.Join(out, "geo-published-at"))
	fmt.Printf("Windows 内置核心就绪：mihomo %s，地理库 %s\n", version, strings.TrimSpace(string(published)))
	return nil
}

func readLock(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = v
		}
	}
	return values, scanner.Err()
}

func download(url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fetchCore(out, arch, version, asset, want string) error {
	dest := filepath.Join(out, "mihomo-windows-"+arch+".exe")
	stamp := dest + ".sha256"
	if got, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(got)) == want {
		if _, err := os.Stat(dest); err == nil {
			fmt.Printf("mihomo-windows-%s %s 已就绪\n", arch, version)
			return nil
		}
	}
	fmt.Printf("下载 %s …\n", asset)
	data, err := download("https://github.com/MetaCubeX/mihomo/releases/download/"+version+"/"+asset, 200<<20)
	if err != nil {
		return err
	}
	if got := sha(data); got != want {
		return fmt.Errorf("校验失败：%s\n  期望 %s\n  实际 %s", asset, want, got)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range archive.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".exe") {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		exe, err := io.ReadAll(io.LimitReader(r, 300<<20))
		r.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, exe, 0o755); err != nil {
			return err
		}
		return os.WriteFile(stamp, []byte(want), 0o644)
	}
	return fmt.Errorf("%s 里没有 .exe", asset)
}

// fetchLicense：许可证文本随安装包分发（GPL 要求）；哈希与 core.lock 不一致说明上游许可证变了，必须人工确认。
func fetchLicense(out, version, want string) error {
	dest := filepath.Join(out, "mihomo-LICENSE.txt")
	if data, err := os.ReadFile(dest); err == nil && sha(data) == want {
		return nil
	}
	data, err := download("https://raw.githubusercontent.com/MetaCubeX/mihomo/"+version+"/LICENSE", 1<<20)
	if err != nil {
		return err
	}
	if got := sha(data); got != want {
		return fmt.Errorf("mihomo %s 的 LICENSE 与 core.lock 记录不一致（期望 %s，实际 %s），请人工确认许可证后更新", version, want, got)
	}
	return os.WriteFile(dest, data, 0o644)
}

// fetchGeo：meta-rules-dat 的滚动 latest 发布每天更新，不写进 lock；用 GitHub API 给出的摘要校验完整性。
func fetchGeo(out string, refresh bool) error {
	names := []string{"geoip.metadb", "geosite.dat"}
	missing := refresh
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(out, n)); err != nil {
			missing = true
		}
	}
	if !missing {
		return nil
	}
	body, err := download("https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/latest", 4<<20)
	if err != nil {
		return err
	}
	var release struct {
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return err
	}
	for _, n := range names {
		want := ""
		for _, a := range release.Assets {
			if a.Name == n {
				_, want, _ = strings.Cut(a.Digest, ":")
			}
		}
		if want == "" {
			return errors.New("GitHub 没有给出 " + n + " 的摘要")
		}
		fmt.Printf("下载 %s …\n", n)
		data, err := download("https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/"+n, 200<<20)
		if err != nil {
			return err
		}
		if got := sha(data); got != want {
			return fmt.Errorf("校验失败：%s（期望 %s，实际 %s）", n, want, got)
		}
		if err := os.WriteFile(filepath.Join(out, n), data, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(out, "geo-published-at"), []byte(release.PublishedAt+"\n"), 0o644)
}
