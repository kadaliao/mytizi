// Package svcctl 是 App 侧对后台服务的管理：查询状态、无需管理员地启动、以及弹 UAC 的"修复"。
package svcctl

import "errors"

// State 是服务在服务管理器里的状态。
type State string

const (
	NotInstalled State = "not-installed"
	Stopped      State = "stopped"
	Starting     State = "starting"
	Running      State = "running"
	Stopping     State = "stopping"
	Unknown      State = "unknown"
)

// Describe 返回给人看的说明。
func (s State) Describe() string {
	switch s {
	case NotInstalled:
		return "未安装"
	case Stopped:
		return "已停止"
	case Starting:
		return "正在启动"
	case Running:
		return "运行中"
	case Stopping:
		return "正在停止"
	default:
		return "未知"
	}
}

// ErrCancelled 表示用户在 UAC 弹窗里点了"否"。
var ErrCancelled = errors.New("已取消管理员授权")
