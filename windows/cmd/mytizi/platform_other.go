//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/kadaliao/mytizi/windows/internal/paths"
)

// 非 Windows 平台只用于开发时预览界面（macOS 上 `go run ./cmd/mytizi`）。

const (
	windowWidth  = 360
	windowHeight = 580 + 28
)

func webviewDataDir(user paths.User) string { return filepath.Join(filepath.Dir(user.Logs), "WebView") }

func fixClientSize(*application.WebviewWindow) {}
func attachConsole()                           {}
func localDNS() []string                       { return nil }
func tailscalePresent() bool                   { return false }
func openURL(u string) error                   { return exec.Command("open", u).Start() }
func openFile(p string) error                  { return exec.Command("open", "-t", p).Start() }
func revealFile(p string) error                { return exec.Command("open", "-R", p).Start() }
func prefersDark() bool                        { return false }
func launchAtLogin() bool                      { return false }
func setLaunchAtLogin(bool) error              { return errors.New("只支持 Windows") }
