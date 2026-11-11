//go:build !windows

package service

import (
	"os"
	"syscall"

	"github.com/kadaliao/mytizi/windows/internal/paths"
)

// DefaultPlatform 在非 Windows 平台上只建目录（开发与测试用）。
type DefaultPlatform struct{}

func (DefaultPlatform) PrepareDirs(sys paths.System) error {
	for _, dir := range []string{sys.Base, sys.LogDir, sys.CoreHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (DefaultPlatform) AfterCoreStopped() {}

// terminate 先礼后兵：SIGTERM，超时由调用方补 SIGKILL。
func terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }
