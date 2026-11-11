//go:build windows

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
)

// 窗口客户区是 360×580（与 macOS 版相同）。Wails 的 Width/Height 是含标题栏的外框，
// 先给一个近似值，窗口出现后再按实际边框精确修正（fixClientSize）。
const (
	clientWidth  = 360
	clientHeight = 580
	windowWidth  = clientWidth + 16
	windowHeight = clientHeight + 39
)

func webviewDataDir(user paths.User) string {
	return filepath.Join(filepath.Dir(user.Logs), "WebView2")
}

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procGetClientRect = user32.NewProc("GetClientRect")
	procGetWindowRect = user32.NewProc("GetWindowRect")
	procSetWindowPos  = user32.NewProc("SetWindowPos")
	procGetDpiForWnd  = user32.NewProc("GetDpiForWindow")
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
)

type rect struct{ Left, Top, Right, Bottom int32 }

// fixClientSize 把窗口外框调整到"客户区正好 360×580 逻辑像素"。
func fixClientSize(w *application.WebviewWindow) {
	hwnd := uintptr(w.NativeWindow())
	if hwnd == 0 {
		return
	}
	var client, outer rect
	if r, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client))); r == 0 {
		return
	}
	if r, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&outer))); r == 0 {
		return
	}
	dpi := uint32(96)
	if procGetDpiForWnd.Find() == nil {
		if d, _, _ := procGetDpiForWnd.Call(hwnd); d != 0 {
			dpi = uint32(d)
		}
	}
	wantW := int32(clientWidth * dpi / 96)
	wantH := int32(clientHeight * dpi / 96)
	frameW := (outer.Right - outer.Left) - (client.Right - client.Left)
	frameH := (outer.Bottom - outer.Top) - (client.Bottom - client.Top)
	if client.Right-client.Left == wantW && client.Bottom-client.Top == wantH {
		return
	}
	const swpNoMove, swpNoZOrder, swpNoActivate = 0x0002, 0x0004, 0x0010
	procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(wantW+frameW), uintptr(wantH+frameH), swpNoMove|swpNoZOrder|swpNoActivate)
}

// attachConsole：MyTizi.exe 是图形界面程序，从命令行运行 --diagnose 时把输出接到父进程的控制台。
// 已经重定向到文件或管道（`> diag.txt`、`| Out-String`）时保持不动。
func attachConsole() {
	if handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && handle != 0 && handle != windows.InvalidHandle {
		if t, err := windows.GetFileType(handle); err == nil && t != windows.FILE_TYPE_UNKNOWN {
			return
		}
	}
	const attachParentProcess = ^uintptr(0)
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	_ = windows.SetConsoleOutputCP(65001)
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
	os.Stdout.WriteString("\r\n")
}

// MARK: 网络信息

type adapter struct {
	name, description string
	ifType, oper      uint32
	metric            uint32
	hasGateway        bool
	dns               []net.IP
}

func adapters() []adapter {
	size := uint32(16 << 10)
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		head := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0, head, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil
		}
		var out []adapter
		for a := head; a != nil; a = a.Next {
			item := adapter{
				name: windows.UTF16PtrToString(a.FriendlyName), description: windows.UTF16PtrToString(a.Description),
				ifType: a.IfType, oper: a.OperStatus, metric: a.Ipv4Metric, hasGateway: a.FirstGatewayAddress != nil,
			}
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				if ip := d.Address.IP(); ip != nil {
					item.dns = append(item.dns, ip)
				}
			}
			out = append(out, item)
		}
		return out
	}
	return nil
}

const (
	ifTypeSoftwareLoopback = 24
	ifTypeTunnel           = 131
	ifTypePropVirtual      = 53
)

// localDNS 返回物理网卡（有默认网关的、按跃点数排序）上的 IPv4 DNS，也就是路由器给的 DNS。
func localDNS() []string {
	list := adapters()
	sort.SliceStable(list, func(i, j int) bool { return list[i].metric < list[j].metric })
	var out []string
	seen := map[string]bool{}
	for _, a := range list {
		if a.oper != windows.IfOperStatusUp || !a.hasGateway || a.name == appinfo.TunDevice ||
			a.ifType == ifTypeSoftwareLoopback || a.ifType == ifTypeTunnel || a.ifType == ifTypePropVirtual {
			continue
		}
		for _, ip := range a.dns {
			v4 := ip.To4()
			if v4 == nil || v4.IsLoopback() || (v4[0] == 198 && v4[1] == 18) || v4.String() == "100.100.100.100" {
				continue
			}
			if !seen[v4.String()] && len(out) < 4 {
				seen[v4.String()] = true
				out = append(out, v4.String())
			}
		}
	}
	return out
}

// tailscalePresent：Tailscale 的网卡在线。
func tailscalePresent() bool {
	for _, a := range adapters() {
		if a.oper == windows.IfOperStatusUp &&
			(strings.Contains(strings.ToLower(a.name), "tailscale") || strings.Contains(strings.ToLower(a.description), "tailscale")) {
			return true
		}
	}
	return false
}

// MARK: 打开文件与网页

func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func openURL(u string) error {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return os.ErrInvalid
	}
	return shellOpen(u)
}

func openFile(path string) error { return shellOpen(path) }

// revealFile 在资源管理器里打开所在文件夹并选中它。
func revealFile(path string) error {
	cmd := exec.Command("explorer.exe")
	// explorer 自己解析 /select, 参数，不能让 Go 给整个参数加引号。
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}

// prefersDark 读取"选择默认应用模式"（深色/浅色）。
func prefersDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// MARK: 开机启动

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func launchCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return `"` + exe + `" --hidden`
}

func launchAtLogin() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appinfo.DisplayName)
	return err == nil
}

func setLaunchAtLogin(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(appinfo.DisplayName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue(appinfo.DisplayName, launchCommand())
}
