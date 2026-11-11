// mytizi-service：以 SYSTEM 身份运行的后台服务，看管内置 mihomo。
//
//	mytizi-service.exe                 由服务管理器启动（正常情况）
//	mytizi-service.exe install         注册并启动服务（需要管理员；安装程序与 App 的"修复"调用）
//	mytizi-service.exe uninstall       停止并删除服务，清理 %ProgramData%\MyTizi（卸载程序调用）
//	mytizi-service.exe run             前台运行（调试用，需要管理员）
//	mytizi-service.exe version
package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/service"
)

func main() {
	if isWindowsService() {
		runAsService()
		return
	}
	command := ""
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var err error
	switch command {
	case "install":
		err = install(os.Args[2:])
	case "uninstall":
		err = uninstall()
	case "run":
		err = runForeground()
	case "version", "--version":
		fmt.Printf("mytizi-service %s ipc %d\n", appinfo.Version, appinfo.IPCVersion)
	default:
		fmt.Fprintln(os.Stderr, "用法：mytizi-service install | uninstall | run | version")
		os.Exit(64)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败：", err)
		os.Exit(1)
	}
}

// server 是服务的运行实例。
type server struct {
	supervisor *service.Supervisor
	log        *rotlog.Log
	stop       func()
}

func startServer() (*server, error) {
	sys := paths.DefaultSystem()
	platform := service.DefaultPlatform{}
	if err := platform.PrepareDirs(sys); err != nil {
		return nil, fmt.Errorf("准备系统目录失败：%w", err)
	}
	log := rotlog.New(sys.HelperLog(), 1<<20, 2)
	coreLog := rotlog.New(sys.CoreLog(), 5<<20, 2)
	components, tun := paths.ComponentsIn(paths.InstallDir()), true
	// 只在非 Windows 的开发环境里生效：不开 TUN（不需要 root），核心与地理库取自指定目录。
	if runtime.GOOS != "windows" {
		if dir := os.Getenv("MYTIZI_CORE_DIR"); dir != "" {
			components = paths.Components{Dir: dir}
		}
		tun = os.Getenv("MYTIZI_DEV_NO_TUN") == ""
	}
	supervisor := service.New(service.Options{
		System: sys, Components: components, TUN: tun,
		Log: log, CoreLog: coreLog, Platform: platform,
	})
	listener, err := ipc.Listen()
	if err != nil {
		log.Line("无法创建命名管道：%v", err)
		return nil, fmt.Errorf("无法创建命名管道 %s：%w", appinfo.PipeName, err)
	}
	log.Line("后台服务已就绪 %s（pid %d），管道 %s", appinfo.Version, os.Getpid(), appinfo.PipeName)
	go func() { _ = ipc.Serve(listener, supervisor.Handle, log.Line) }()
	return &server{
		supervisor: supervisor,
		log:        log,
		stop: func() {
			log.Line("服务停止：断开并退出")
			supervisor.Shutdown()
			listener.Close()
			coreLog.Close()
		},
	}, nil
}

// onResume 在系统从睡眠中唤醒后调用。
func (s *server) onResume() {
	go func() {
		time.Sleep(3 * time.Second)
		s.supervisor.ResetConnections()
	}()
}

func runForeground() error {
	srv, err := startServer()
	if err != nil {
		return err
	}
	fmt.Println("后台服务在前台运行，按 Ctrl+C 停止。日志：", paths.DefaultSystem().LogDir)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	srv.stop()
	return nil
}
