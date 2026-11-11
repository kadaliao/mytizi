//go:build windows

package svcctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

func open(access uint32) (windows.Handle, windows.Handle, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, err
	}
	name, _ := windows.UTF16PtrFromString(appinfo.ServiceName)
	service, err := windows.OpenService(scm, name, access)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return 0, 0, err
	}
	return scm, service, nil
}

// Query 返回服务状态（普通用户即可）。
func Query() State {
	scm, service, err := open(windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return NotInstalled
	}
	if err != nil {
		return Unknown
	}
	defer windows.CloseServiceHandle(scm)
	defer windows.CloseServiceHandle(service)
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(service, &status); err != nil {
		return Unknown
	}
	switch status.CurrentState {
	case windows.SERVICE_STOPPED:
		return Stopped
	case windows.SERVICE_START_PENDING:
		return Starting
	case windows.SERVICE_RUNNING:
		return Running
	case windows.SERVICE_STOP_PENDING:
		return Stopping
	default:
		return Unknown
	}
}

// Start 启动已安装但停止的服务。安装程序给本机登录用户授予了启动权限，不需要 UAC。
func Start(timeout time.Duration) error {
	scm, service, err := open(windows.SERVICE_START | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(scm)
	defer windows.CloseServiceHandle(service)
	if err := windows.StartService(service, 0, nil); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var status windows.SERVICE_STATUS
		if err := windows.QueryServiceStatus(service, &status); err == nil && status.CurrentState == windows.SERVICE_RUNNING {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("等待后台服务启动超时")
}

// shellExecuteInfo 对应 SHELLEXECUTEINFOW（字段顺序与对齐和 C 结构一致）。
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const seeMaskNoCloseProcess = 0x00000040

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// RepairElevated 以管理员身份运行安装目录里的 `mytizi-service.exe install`（会弹 UAC），等它结束。
func RepairElevated(installDir string) error {
	exe := filepath.Join(installDir, "mytizi-service.exe")
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("找不到 %s，请重新运行安装程序", exe)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString("install")
	dir, _ := windows.UTF16PtrFromString(installDir)
	info := shellExecuteInfo{
		fMask: seeMaskNoCloseProcess, lpVerb: verb, lpFile: file, lpParameters: params, lpDirectory: dir,
		nShow: windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return ErrCancelled
		}
		return fmt.Errorf("无法以管理员身份运行：%w", callErr)
	}
	if info.hProcess == 0 {
		return nil
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, 120*1000); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err == nil && code != 0 {
		return fmt.Errorf("安装后台组件失败（退出码 %d），详情见 %%ProgramData%%\\MyTizi\\logs", code)
	}
	return nil
}
