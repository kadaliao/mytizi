//go:build !windows

package svcctl

import (
	"errors"
	"time"
)

// 非 Windows 平台（开发）：假装服务由开发者手动运行（mytizi-service run）。

func Query() State                { return Unknown }
func Start(time.Duration) error   { return errors.New("只支持 Windows") }
func RepairElevated(string) error { return errors.New("只支持 Windows") }
