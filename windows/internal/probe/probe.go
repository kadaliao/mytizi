// Package probe 用内置 mihomo 解析订阅。
//
// 以普通用户身份起一个"不监听代理端口、不开 TUN"的临时实例，让 mihomo 自己解析订阅
// （Clash YAML、base64 链接列表、各种协议都支持），再从控制接口读出节点。
// App 不需要实现任何订阅格式，而且和真正连接时使用的解析器完全一致。
package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/mihomo"
	"github.com/kadaliao/mytizi/windows/internal/proc"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/yamlx"
)

var ErrCoreMissing = errors.New("找不到内置核心，请重新安装 App。")

// Error 带着核心日志的末尾。
type Error struct {
	Reason string
	Log    string
}

func (e *Error) Error() string { return e.Reason + "：" + e.Log }

var notNodes = map[string]bool{"Direct": true, "Reject": true, "RejectDrop": true, "Compatible": true, "Pass": true, "Dns": true}

// Nodes 解析订阅内容，返回其中的线路。
func Nodes(ctx context.Context, body []byte, core, workDir string, timeout time.Duration) ([]settings.Node, error) {
	if _, err := os.Stat(core); err != nil {
		return nil, ErrCoreMissing
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(workDir, "probe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, "providers"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "providers", "sub.yaml"), body, 0o600); err != nil {
		return nil, err
	}
	port := proc.FreePort()
	secret := randomHex(16)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlx.Render(config.BuildProbe(port, secret))), 0o600); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "core.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.Command(core, "-d", dir, "-f", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "SAFE_PATHS="+dir)
	proc.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动内置核心：%w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Kill()
			<-exited
		}
	}()

	tail := func() string {
		data, _ := os.ReadFile(logPath)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 6 {
			lines = lines[len(lines)-6:]
		}
		return strings.Join(lines, "\n")
	}

	api := mihomo.New(port, secret)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return nil, &Error{Reason: "解析订阅时核心异常退出", Log: tail()}
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if provider, err := api.Provider(ctx, config.ProviderName); err == nil {
			var nodes []settings.Node
			for _, p := range provider.Proxies {
				if !notNodes[p.Type] {
					nodes = append(nodes, settings.Node{Name: p.Name, Type: p.Type, UDP: p.UDP})
				}
			}
			return nodes, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, &Error{Reason: "解析订阅超时", Log: tail()}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
