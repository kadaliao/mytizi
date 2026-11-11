package diag

import (
	"context"
	"os"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/mihomo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
)

// Report 一键生成报告文本：采集、渲染、脱敏。base 由 App 填入它自己知道的部分。
func Report(ctx context.Context, base Input, client *ipc.Client, full, runSelfTest bool) string {
	return Render(Gather(ctx, base, client, runSelfTest), full)
}

// Gather 补齐诊断需要的一切：向后台服务问状态、读系统网络、读日志、（连接中时）跑实机自检。
func Gather(ctx context.Context, base Input, client *ipc.Client, runSelfTest bool) Input {
	in := base
	in.Now = time.Now()
	if in.UserPaths.Support == "" {
		in.UserPaths = paths.DefaultUser()
	}
	if in.System.Base == "" {
		in.System = paths.DefaultSystem()
	}

	in.ServiceState = svcctl.Query()
	if client != nil {
		if hello, err := client.Hello(ctx); err != nil {
			in.ServiceError = err.Error()
		} else {
			in.Hello = hello
			if status, err := client.Status(ctx); err != nil {
				in.ServiceError = err.Error()
			} else {
				in.Status = status
			}
		}
	}
	if in.InstallDir == "" {
		if in.Hello != nil && in.Hello.InstallDir != "" {
			in.InstallDir = in.Hello.InstallDir
		} else {
			in.InstallDir = paths.InstallDir()
		}
	}
	if info, err := os.Stat(geoFile(in.InstallDir)); err == nil {
		in.GeoDate = info.ModTime()
	}

	corePID := 0
	if in.Status != nil {
		corePID = in.Status.CorePID
	}
	in.Facts = CollectFacts(ctx, corePID)

	in.AppLog = rotlog.Tail(in.UserPaths.AppLog(), 200, 0)
	in.HelperLog = rotlog.Tail(in.System.HelperLog(), 200, 0)
	in.CoreLog = rotlog.Tail(in.System.CoreLog(), 400, 0)

	if in.IsRunning() && in.Status.ControllerPort > 0 && in.Status.ControllerSecret != "" {
		api := mihomo.New(in.Status.ControllerPort, in.Status.ControllerSecret)
		if version, err := api.Version(ctx); err == nil {
			in.CoreVersion = version
		}
		if provider, err := api.Provider(ctx, config.ProviderName); err == nil {
			in.Nodes = nil
			for _, p := range provider.Proxies {
				in.Nodes = append(in.Nodes, NodeDelay{Name: p.Name, Type: p.Type, Delay: p.LastDelay()})
			}
		}
		if runSelfTest {
			coreLog := in.System.CoreLog()
			in.SelfTest = RunSelfTest(ctx, func() string { return rotlog.Tail(coreLog, 400, 0) },
				len(in.Facts.GlobalIPv6Adapters()) > 0)
			// 自检会产生新的日志，重新读一次给报告用。
			in.CoreLog = rotlog.Tail(coreLog, 400, 0)
		}
	} else if len(in.Nodes) == 0 && in.Settings.Subscription != nil {
		for _, n := range in.Settings.Subscription.Nodes {
			in.Nodes = append(in.Nodes, NodeDelay{Name: n.Name, Type: n.Type})
		}
	}
	return in
}
