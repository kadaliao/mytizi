// Package proc 是启动子进程时的平台差异：Windows 上不弹黑色控制台窗口、
// 后台服务里把核心放进"关掉就一起结束"的作业对象。
package proc

import (
	"net"
	"os/exec"
	"strconv"
)

// Hide 让子进程不显示控制台窗口（Windows）；其他平台无操作。
func Hide(cmd *exec.Cmd) { hide(cmd) }

// FreePort 在回环地址上找一个空闲端口：先试偏好端口，再让系统分配。
func FreePort(preferred ...int) int {
	for _, p := range preferred {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
			l.Close()
			return p
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
