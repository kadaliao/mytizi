// MyTizi：Windows 版主程序。同一个可执行文件既是图形界面，也是命令行诊断工具：
//
//	MyTizi.exe --diagnose [--full] [--no-selftest]   打印诊断报告（给没有图形界面的 AI / 同事用）
//	MyTizi.exe --check-subscription <链接>           用和界面相同的代码下载并解析订阅，打印线路（不保存）
//	MyTizi.exe --hidden                              开机自启时使用：只在托盘里运行，不弹窗口
package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appcore"
	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/diag"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/probe"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/subscription"
	"github.com/kadaliao/mytizi/windows/internal/svcctl"
	"github.com/kadaliao/mytizi/windows/internal/update"
)

//go:embed all:frontend
var frontendFiles embed.FS

//go:embed icons
var iconFiles embed.FS

func main() {
	args := os.Args[1:]
	for i, a := range args {
		switch a {
		case "--diagnose":
			os.Exit(runDiagnose(args))
		case "--check-subscription":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "用法：MyTizi --check-subscription <订阅链接>")
				os.Exit(64)
			}
			os.Exit(runCheckSubscription(args[i+1]))
		}
	}
	hidden := false
	for _, a := range args {
		if a == "--hidden" {
			hidden = true
		}
	}
	runGUI(hidden)
}

// coreBinary 是探测订阅用的内置核心：安装目录里的；开发运行时可以用环境变量指定。
func coreBinary() string {
	if dev := os.Getenv("MYTIZI_CORE"); dev != "" {
		return dev
	}
	return paths.ComponentsIn(paths.InstallDir()).Core()
}

func probeNodes(ctx context.Context, body []byte, cacheDir string) ([]settings.Node, error) {
	return probe.Nodes(ctx, body, coreBinary(), cacheDir, 12*time.Second)
}

func fetch(ctx context.Context, link string) (*subscription.Fetched, error) {
	u, err := subscription.NormalizeURL(link)
	if err != nil {
		return nil, err
	}
	return subscription.Fetch(ctx, u, 20*time.Second)
}

// newDeps 组装状态机需要的真实实现；界面相关的几项由 runGUI 补上。
func newDeps(user paths.User) appcore.Deps {
	checker := update.NewChecker()
	client := ipc.NewClient()
	return appcore.Deps{
		Client:     client,
		Store:      settings.NewStore(user),
		Paths:      user,
		InstallDir: paths.InstallDir(),
		Version:    appinfo.Version,
		Probe: func(ctx context.Context, body []byte) ([]settings.Node, error) {
			return probeNodes(ctx, body, user.Cache)
		},
		Fetch: fetch,
		Diagnose: func(ctx context.Context, full bool, info appcore.DiagContext) string {
			return diag.Report(ctx, diagInput(user, info), client, full, true)
		},
		CheckUpdate: func(ctx context.Context) (*appcore.UpdateInfo, error) {
			info, err := checker.Latest(ctx)
			if err != nil {
				return nil, err
			}
			return &appcore.UpdateInfo{Version: info.Version, ReleaseURL: info.ReleaseURL, DownloadURL: info.DownloadURL}, nil
		},
		IsNewer:       update.IsNewer,
		ServiceState:  svcctl.Query,
		StartService:  func() error { return svcctl.Start(15 * time.Second) },
		RepairService: func() error { return svcctl.RepairElevated(paths.InstallDir()) },
		LocalDNS:      localDNS,
		Tailscale:     tailscalePresent,
		OpenURL:       openURL,
		OpenFile:      openFile,
		RevealFile:    revealFile,
		LaunchAtLogin: launchAtLogin,
		SetLaunchAtLogin: func(on bool) error {
			return setLaunchAtLogin(on)
		},
	}
}

func diagInput(user paths.User, info appcore.DiagContext) diag.Input {
	nodes := make([]diag.NodeDelay, 0, len(info.Nodes))
	for _, n := range info.Nodes {
		nodes = append(nodes, diag.NodeDelay{Name: n.Name, Type: n.Type, Delay: n.Delay})
	}
	return diag.Input{
		AppVersion: appinfo.Version, AppPhase: info.AppPhase, LastError: info.LastError,
		Settings: info.Settings, Nodes: nodes, CustomRuleCount: info.CustomRuleCount,
		CustomRulesError: info.CustomRulesError, UserPaths: user,
	}
}

// MARK: 命令行

func runDiagnose(args []string) int {
	attachConsole()
	full, selfTest := false, true
	for _, a := range args {
		switch a {
		case "--full":
			full = true
		case "--no-selftest":
			selfTest = false
		}
	}
	user := paths.DefaultUser()
	store := settings.NewStore(user)
	s := store.Load()
	info := appcore.DiagContext{AppPhase: "（命令行，未运行界面）", Settings: s}
	if rules, err := store.LoadCustomRules(); err != nil {
		info.CustomRulesError = err.Error()
	} else {
		info.CustomRuleCount = len(rules)
	}
	if s.Subscription != nil {
		for _, n := range s.Subscription.Nodes {
			info.Nodes = append(info.Nodes, appcore.NodeRow{Name: n.Name, Type: n.Type})
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fmt.Println(diag.Report(ctx, diagInput(user, info), ipc.NewClient(), full, selfTest))
	return 0
}

func runCheckSubscription(link string) int {
	attachConsole()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fetched, err := fetch(ctx, link)
	if err != nil {
		fmt.Println("失败：", appcore.Message(err))
		return 1
	}
	usage := "-"
	if fetched.Usage != nil {
		usage = fmt.Sprintf("%d/%d", fetched.Usage.Used(), fetched.Usage.Total)
	}
	fmt.Printf("下载成功：%d 字节，标题 %s，流量 %s\n", len(fetched.Body), orDash(fetched.Title), usage)
	work, err := os.MkdirTemp("", "mytizi-check-")
	if err != nil {
		fmt.Println("失败：", err)
		return 1
	}
	defer os.RemoveAll(work)
	nodes, err := probeNodes(ctx, fetched.Body, work)
	if err != nil {
		fmt.Println("失败：", err)
		return 1
	}
	fmt.Printf("解析出 %d 条线路：\n", len(nodes))
	for i, n := range nodes {
		if i >= 50 {
			break
		}
		udp := ""
		if n.UDP {
			udp = " udp"
		}
		fmt.Printf("  - %s  [%s]%s\n", n.Name, n.Type, udp)
	}
	if len(nodes) == 0 {
		return 2
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// frontendFS 返回前端根目录。
func frontendFS() fs.FS {
	sub, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		panic(err)
	}
	return sub
}

func icon(name string) []byte {
	data, _ := iconFiles.ReadFile(filepath.ToSlash(filepath.Join("icons", name)))
	return data
}
