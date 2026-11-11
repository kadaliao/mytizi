//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/service"
)

// 服务的访问控制：在默认值基础上允许本机登录用户（IU）启动服务（RP），
// 这样服务意外停止后，App 不需要管理员授权就能把它拉起来。停止、修改仍然只有管理员能做。
const serviceSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

const (
	pbtAPMResumeSuspend   = 0x7
	pbtAPMResumeAutomatic = 0x12
)

func isWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

type handler struct{}

func (handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	srv, err := startServer()
	if err != nil {
		if elog, e := eventlog.Open(appinfo.ServiceName); e == nil {
			_ = elog.Error(1, err.Error())
			elog.Close()
		}
		return true, 1
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPowerEvent}
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			status <- request.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending, WaitHint: 15000}
			srv.stop()
			return false, 0
		case svc.PowerEvent:
			if request.EventType == pbtAPMResumeSuspend || request.EventType == pbtAPMResumeAutomatic {
				srv.log.Line("系统从睡眠中唤醒")
				srv.onResume()
			}
		}
	}
	return false, 0
}

func runAsService() {
	_ = svc.Run(appinfo.ServiceName, handler{})
}

// install 注册（或更新）服务并启动。必须以管理员身份运行。
func install(args []string) error {
	allowAnyPath := false
	for _, a := range args {
		if a == "--allow-any-path" {
			allowAnyPath = true
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)
	// 服务以 SYSTEM 身份运行这个文件：它必须在普通用户改不了的位置，否则就是现成的提权漏洞。
	if !allowAnyPath {
		programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(strings.ToLower(exe), strings.ToLower(programFiles)+`\`) {
			return fmt.Errorf("后台组件必须安装在 %s 下（当前在 %s）。请使用安装程序", programFiles, exe)
		}
	}
	components := paths.ComponentsIn(filepath.Dir(exe))
	if _, err := os.Stat(components.Core()); err != nil {
		return fmt.Errorf("缺少内置核心 %s", components.Core())
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败（需要管理员权限）：%w", err)
	}
	defer m.Disconnect()

	config := mgr.Config{
		DisplayName:  appinfo.ServiceDisplayName,
		Description:  appinfo.ServiceDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		SidType:      windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}
	s, err := m.OpenService(appinfo.ServiceName)
	if err == nil {
		// 已存在：先停（会断开连接），再改路径与配置。
		_ = stopService(s)
		current, err := s.Config()
		if err != nil {
			s.Close()
			return err
		}
		current.BinaryPathName = `"` + exe + `"`
		current.DisplayName = config.DisplayName
		current.Description = config.Description
		current.StartType = config.StartType
		current.SidType = config.SidType
		if err := s.UpdateConfig(current); err != nil {
			s.Close()
			return fmt.Errorf("更新服务配置失败：%w", err)
		}
	} else {
		s, err = m.CreateService(appinfo.ServiceName, exe, config)
		if err != nil {
			return fmt.Errorf("创建服务失败：%w", err)
		}
	}
	defer s.Close()

	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)
	if sd, err := windows.SecurityDescriptorFromString(serviceSDDL); err == nil {
		if dacl, _, err := sd.DACL(); err == nil {
			_ = windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
		}
	}
	_ = eventlog.InstallAsEventCreate(appinfo.ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info)

	if err := (service.DefaultPlatform{}).PrepareDirs(paths.DefaultSystem()); err != nil {
		return err
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动服务失败：%w", err)
	}
	if err := waitState(s, svc.Running, 20*time.Second); err != nil {
		return err
	}
	fmt.Println("后台服务已安装并启动：", exe)
	return nil
}

// uninstall 停止并删除服务，清理系统数据（用户的订阅与设置保留在 %APPDATA%）。
func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败（需要管理员权限）：%w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(appinfo.ServiceName)
	if err == nil {
		_ = stopService(s)
		if err := s.Delete(); err != nil {
			s.Close()
			return fmt.Errorf("删除服务失败：%w", err)
		}
		s.Close()
	}
	_ = eventlog.Remove(appinfo.ServiceName)
	if err := os.RemoveAll(paths.DefaultSystem().Base); err != nil {
		fmt.Fprintln(os.Stderr, "清理系统数据失败：", err)
	}
	fmt.Println("后台服务已卸载")
	return nil
}

func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	return waitState(s, svc.Stopped, 30*time.Second)
}

func waitState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("等待服务状态 %d 超时", want)
}
