//go:build !windows

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// 非 Windows 平台（开发与测试）用 Unix socket 代替命名管道。
func socketPath() string { return filepath.Join(os.TempDir(), "mytizi-helper.sock") }

func Listen() (net.Listener, error) {
	_ = os.Remove(socketPath())
	return net.Listen("unix", socketPath())
}

func DialService(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socketPath())
}

func isNotRunning(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}
