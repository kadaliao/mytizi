//go:build !windows

package proc

import "os/exec"

func hide(*exec.Cmd) {}

// KillOnCloseJob 在非 Windows 平台上没有对应物（开发与测试用）。
type KillOnCloseJob struct{}

func NewKillOnCloseJob() (*KillOnCloseJob, error) { return &KillOnCloseJob{}, nil }
func (j *KillOnCloseJob) Assign(int) error        { return nil }
func (j *KillOnCloseJob) Close() error            { return nil }
