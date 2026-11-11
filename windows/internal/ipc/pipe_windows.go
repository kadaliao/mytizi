//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

// PipeSDDL：SYSTEM 与管理员完全控制；本机登录的用户（INTERACTIVE）可以读写。
// 远程网络登录、其他服务账户都连不上。
const PipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

// Listen 创建服务端命名管道。go-winio 用 FILE_FLAG_FIRST_PIPE_INSTANCE 创建第一个实例，
// 同名管道已被别人占用时会失败，而不是悄悄共用。
func Listen() (net.Listener, error) {
	return winio.ListenPipe(appinfo.PipeName, &winio.PipeConfig{
		SecurityDescriptor: PipeSDDL,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
}

// DialService 连接服务的命名管道，并确认管道属于 SYSTEM 或管理员：
// 服务没在运行时，普通用户的程序可能抢先创建同名管道来冒充它，骗取订阅链接。
func DialService(ctx context.Context) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, appinfo.PipeName)
	if err != nil {
		return nil, err
	}
	if err := verifyPipeOwner(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func verifyPipeOwner(conn net.Conn) error {
	withFd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法检查管道的所有者")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(withFd.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("读取管道所有者失败：%w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("读取管道所有者失败：%w", err)
	}
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return nil
	}
	return fmt.Errorf("命名管道 %s 不属于系统服务（所有者 %s），拒绝连接", appinfo.PipeName, owner.String())
}

func isNotRunning(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, winio.ErrTimeout) ||
		errors.Is(err, context.DeadlineExceeded)
}
