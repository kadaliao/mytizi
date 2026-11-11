//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/kadaliao/mytizi/windows/internal/paths"
)

const (
	// 基础目录与日志目录：SYSTEM、管理员完全控制；普通用户只读（App 和排查用的 AI 要读日志）。
	sddlReadable = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"
	// 核心工作目录：只有 SYSTEM 与管理员（配置里有控制接口的密钥）。
	sddlPrivate = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
)

// DefaultPlatform 是 Windows 上的实现。
type DefaultPlatform struct{}

// PrepareDirs 创建 %ProgramData%\MyTizi 及子目录，并强制设置权限。
//
// %ProgramData% 允许普通用户新建文件夹：如果有人抢先建了 MyTizi（或放了一个指向别处的联接点），
// 以 SYSTEM 身份往里写日志就等于替他写任意文件。所以所有者不是 SYSTEM/管理员、或者是重解析点的目录，
// 一律挪到一边再重建。
func (DefaultPlatform) PrepareDirs(sys paths.System) error {
	if err := ensureSecureDir(sys.Base, sddlReadable); err != nil {
		return err
	}
	if err := ensureSecureDir(sys.LogDir, sddlReadable); err != nil {
		return err
	}
	return ensureSecureDir(sys.CoreHome, sddlPrivate)
}

func ensureSecureDir(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if trustedDir(path, info) {
			dacl, _, err := sd.DACL()
			if err != nil {
				return err
			}
			return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
		}
		aside := fmt.Sprintf("%s.untrusted-%d", path, time.Now().Unix())
		if err := os.Rename(path, aside); err != nil {
			return fmt.Errorf("%s 的所有者不可信，且无法移走：%w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	if err := windows.CreateDirectory(name, sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("创建 %s 失败：%w", path, err)
	}
	return nil
}

// trustedDir：真实目录（不是联接点/符号链接），且所有者是 SYSTEM 或管理员组。
func trustedDir(path string, info os.FileInfo) bool {
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return false
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok &&
		data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false
	}
	return owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

var procDnsFlushResolverCache = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")

// AfterCoreStopped 清空系统 DNS 缓存：连接期间缓存的都是 198.18.x.x 的 fake-ip，断开后已经没人应答。
func (DefaultPlatform) AfterCoreStopped() {
	if procDnsFlushResolverCache.Find() == nil {
		_, _, _ = procDnsFlushResolverCache.Call()
	}
}

// terminate：Windows 没有 SIGTERM。mihomo 退出时 Wintun 网卡、路由与 WFP 规则都会随进程句柄一起释放。
func terminate(p *os.Process) { _ = p.Kill() }
