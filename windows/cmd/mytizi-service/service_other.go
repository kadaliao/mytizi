//go:build !windows

package main

import "errors"

// 非 Windows 平台只支持 run（开发调试）。

func isWindowsService() bool { return false }
func runAsService()          {}
func install([]string) error { return errors.New("只支持 Windows") }
func uninstall() error       { return errors.New("只支持 Windows") }
