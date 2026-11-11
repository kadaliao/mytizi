// Package service 是后台服务的逻辑：以 SYSTEM 身份看管内置 mihomo。
//
// 三条原则（与 macOS 版相同）：
//  1. 核心与网络接管同生共死。Windows 上这一条由 mihomo 自己保证——TUN 网卡、路由、
//     strict-route 的 WFP 规则都随核心进程消失；服务再用作业对象保证"服务没了，核心也没了"。
//  2. App 看到的"已连接"一定意味着 TUN 网卡真的出现了，而不是"进程起来了"。
//  3. 配置由服务自己生成，App 只能提供订阅原文和几项经过校验的设置。
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/ipc"
	"github.com/kadaliao/mytizi/windows/internal/mihomo"
	"github.com/kadaliao/mytizi/windows/internal/paths"
	"github.com/kadaliao/mytizi/windows/internal/proc"
	"github.com/kadaliao/mytizi/windows/internal/rotlog"
	"github.com/kadaliao/mytizi/windows/internal/subscription"
	"github.com/kadaliao/mytizi/windows/internal/yamlx"
)

// Platform 是与操作系统有关的两件事。
type Platform interface {
	// PrepareDirs 创建系统目录并确保只有 SYSTEM/管理员能写。
	PrepareDirs(paths.System) error
	// AfterCoreStopped 在核心退出后调用（Windows：清空 DNS 缓存里的 fake-ip）。
	AfterCoreStopped()
}

type Options struct {
	System     paths.System
	Components paths.Components
	// TUN 正式运行时为 true；测试里没有管理员权限，关掉 TUN 只验证进程管理。
	TUN      bool
	Log      *rotlog.Log
	CoreLog  *rotlog.Log
	Platform Platform
	// TunTimeout 是等待 TUN 网卡出现的时间（Wintun 第一次创建网卡较慢）。
	TunTimeout time.Duration
	// PickPort 选控制端口；默认让系统分配一个空闲端口。测试时注入。
	PickPort func() int
}

type Supervisor struct {
	opt Options
	// op 串行化 start / stop / updateSubscription。
	op sync.Mutex
	// mu 保护下面的状态；绝不在持有它时做耗时操作。
	mu           sync.Mutex
	phase        ipc.Phase
	cmd          *exec.Cmd
	job          *proc.KillOnCloseJob
	generation   int
	expectedExit bool
	exited       chan struct{}
	startedAt    *time.Time
	port         int
	secret       string
	tunInterface string
	strictRoute  bool
	lastExit     *ipc.CoreExit
}

func New(opt Options) *Supervisor {
	if opt.TunTimeout <= 0 {
		opt.TunTimeout = 20 * time.Second
	}
	if opt.PickPort == nil {
		opt.PickPort = func() int { return proc.FreePort() }
	}
	return &Supervisor{opt: opt, phase: ipc.PhaseStopped}
}

// MARK: 状态

func (s *Supervisor) Status() ipc.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ipc.Status{
		Phase: s.phase, StartedAt: s.startedAt, ControllerPort: s.port,
		TunInterface: s.tunInterface, StrictRoute: s.strictRoute, LastExit: s.lastExit,
	}
	if s.cmd != nil && s.cmd.Process != nil {
		st.CorePID = s.cmd.Process.Pid
	}
	if s.phase == ipc.PhaseRunning {
		st.ControllerSecret = s.secret
	}
	return st
}

func (s *Supervisor) CoreInstalled() bool {
	_, err := os.Stat(s.opt.Components.Core())
	return err == nil
}

func (s *Supervisor) setPhase(p ipc.Phase) {
	s.mu.Lock()
	s.phase = p
	s.mu.Unlock()
}

// Handle 分派一个请求。
func (s *Supervisor) Handle(req ipc.Request) ipc.Response {
	switch req.Method {
	case ipc.MethodHello:
		return ipc.Response{OK: true, Hello: &ipc.Hello{
			IPCVersion: appinfo.IPCVersion, ServicePID: os.Getpid(), ServiceVersion: appinfo.Version,
			CoreInstalled: s.CoreInstalled(), InstallDir: filepath.Dir(s.opt.Components.Dir),
		}}
	case ipc.MethodStatus:
		st := s.Status()
		return ipc.Response{OK: true, Status: &st}
	case ipc.MethodStart:
		if req.Start == nil {
			return ipc.Failure("bad-request", "缺少 start 参数", "")
		}
		s.opt.Log.Line("start：strict-route %v，自定义规则 %d 条", req.Start.StrictRoute, len(req.Start.CustomRules))
		return s.Start(*req.Start)
	case ipc.MethodStop:
		s.opt.Log.Line("stop")
		return s.Stop()
	case ipc.MethodUpdateSubscription:
		return s.UpdateSubscription(req.Subscription)
	default:
		return ipc.Failure("bad-request", "未知的请求 "+string(req.Method), "")
	}
}

// MARK: 启动

func (s *Supervisor) Start(p ipc.StartParams) ipc.Response {
	s.op.Lock()
	defer s.op.Unlock()

	if ph := s.Status().Phase; ph == ipc.PhaseRunning || ph == ipc.PhaseStarting {
		s.opt.Log.Line("收到 start，当前已在运行，先停止旧实例")
		s.stopLocked()
	}
	if !s.CoreInstalled() {
		return ipc.Failure("core-missing", "内置核心没有安装，请重新运行安装程序。", s.opt.Components.Core())
	}
	for _, geo := range []string{s.opt.Components.GeoIP(), s.opt.Components.GeoSite()} {
		if _, err := os.Stat(geo); err != nil {
			return ipc.Failure("core-missing", "缺少地理数据库 "+filepath.Base(geo)+"，请重新运行安装程序。", geo)
		}
	}
	options, err := validate(p)
	if err != nil {
		s.opt.Log.Line("参数校验失败：%v", err)
		return ipc.Failure("bad-request", "连接参数无效："+err.Error(), "")
	}

	s.mu.Lock()
	s.phase = ipc.PhaseStarting
	s.lastExit = nil
	s.tunInterface = ""
	s.mu.Unlock()

	sys := s.opt.System
	if err := s.opt.Platform.PrepareDirs(sys); err != nil {
		s.setPhase(ipc.PhaseStopped)
		return ipc.Failure("write-failed", "准备工作目录失败。", err.Error())
	}
	if err := s.syncGeo(); err != nil {
		s.setPhase(ipc.PhaseStopped)
		return ipc.Failure("write-failed", "复制地理数据库失败。", err.Error())
	}

	options.ControllerPort = s.opt.PickPort()
	options.ControllerSecret = randomHex(24)
	options.TUN = s.opt.TUN
	if err := paths.WriteFileAtomic(filepath.Join(sys.CoreHome, "providers", "sub.yaml"), p.Subscription, 0o600); err != nil {
		s.setPhase(ipc.PhaseStopped)
		return ipc.Failure("write-failed", "写入订阅失败。", err.Error())
	}
	if err := paths.WriteFileAtomic(sys.ConfigPath(), []byte(yamlx.Render(config.Build(options))), 0o600); err != nil {
		s.setPhase(ipc.PhaseStopped)
		return ipc.Failure("write-failed", "写入配置失败。", err.Error())
	}

	if out, err := s.checkConfig(); err != nil {
		s.setPhase(ipc.PhaseStopped)
		s.opt.Log.Line("配置校验失败：%v %s", err, out)
		return ipc.Failure("config-invalid", "配置没有通过核心的校验。", lastChars(out, 2000))
	}

	// 控制端口是先探测再交给核心绑定的，中间可能被别的程序抢走；核心不会因此退出，
	// 只会在日志里报错。看到这条错误就换个端口重来，最多三次。
	var gen int
	for attempt := 1; ; attempt++ {
		var err error
		gen, err = s.launch()
		if err != nil {
			s.setPhase(ipc.PhaseStopped)
			s.opt.Log.Line("启动核心失败：%v", err)
			return ipc.Failure("launch-failed", "无法启动内置核心。", err.Error()+"\n"+s.opt.CoreLog.Tail(20))
		}
		ready, portTaken := s.waitController(options.ControllerPort, options.ControllerSecret, gen, 15*time.Second)
		if ready {
			break
		}
		if portTaken && attempt < 3 {
			s.opt.Log.Line("控制端口 %d 被占用，换一个端口重试", options.ControllerPort)
			s.terminateCore()
			options.ControllerPort = s.opt.PickPort()
			if err := paths.WriteFileAtomic(sys.ConfigPath(), []byte(yamlx.Render(config.Build(options))), 0o600); err != nil {
				s.setPhase(ipc.PhaseStopped)
				return ipc.Failure("write-failed", "写入配置失败。", err.Error())
			}
			continue
		}
		tail := s.opt.CoreLog.Tail(20)
		s.opt.Log.Line("控制接口没有就绪：%s", tail)
		s.stopLocked()
		return ipc.Failure("controller-timeout", "核心启动了，但控制接口没有响应。", tail)
	}

	var tun string
	if s.opt.TUN {
		tun = s.waitInterface(appinfo.TunGateway, gen, s.opt.TunTimeout)
		if tun == "" {
			tail := s.opt.CoreLog.Tail(20)
			s.opt.Log.Line("TUN 网卡没有出现：%s", tail)
			s.stopLocked()
			return ipc.Failure("tun-missing", "没能创建虚拟网卡（TUN）。", tail)
		}
	}

	now := time.Now()
	s.mu.Lock()
	s.phase = ipc.PhaseRunning
	s.startedAt = &now
	s.port = options.ControllerPort
	s.secret = options.ControllerSecret
	s.tunInterface = tun
	s.strictRoute = p.StrictRoute
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.Unlock()
	s.opt.Log.Line("已连接（核心 pid %d，TUN %s）", pid, orDefault(tun, "无"))
	st := s.Status()
	return ipc.Response{OK: true, Status: &st}
}

func (s *Supervisor) checkConfig() (string, error) {
	sys := s.opt.System
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.opt.Components.Core(), "-t", "-d", sys.CoreHome, "-f", sys.ConfigPath())
	cmd.Dir = sys.CoreHome
	cmd.Env = coreEnv(sys.CoreHome)
	proc.Hide(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func coreEnv(home string) []string {
	env := os.Environ()
	return append(env, "SAFE_PATHS="+home)
}

func (s *Supervisor) launch() (int, error) {
	sys := s.opt.System
	cmd := exec.Command(s.opt.Components.Core(), "-d", sys.CoreHome, "-f", sys.ConfigPath())
	cmd.Dir = sys.CoreHome
	cmd.Env = coreEnv(sys.CoreHome)
	cmd.Stdout = s.opt.CoreLog
	cmd.Stderr = s.opt.CoreLog
	proc.Hide(cmd)
	s.opt.CoreLog.Line("%s", launchMarker)

	job, jobErr := proc.NewKillOnCloseJob()
	if jobErr != nil {
		s.opt.Log.Line("创建作业对象失败（不影响连接）：%v", jobErr)
	}
	if err := cmd.Start(); err != nil {
		if job != nil {
			job.Close()
		}
		return 0, err
	}
	if job != nil {
		if err := job.Assign(cmd.Process.Pid); err != nil {
			s.opt.Log.Line("把核心放进作业对象失败（不影响连接）：%v", err)
		}
	}

	s.mu.Lock()
	s.generation++
	gen := s.generation
	s.expectedExit = false
	exited := make(chan struct{})
	s.exited = exited
	s.cmd = cmd
	s.job = job
	s.mu.Unlock()

	go func() {
		err := cmd.Wait()
		s.handleExit(gen, cmd, err, exited)
	}()
	return gen, nil
}

func (s *Supervisor) handleExit(gen int, cmd *exec.Cmd, waitErr error, exited chan struct{}) {
	defer close(exited)
	s.mu.Lock()
	stale := gen != s.generation
	expected := s.expectedExit
	var job *proc.KillOnCloseJob
	if !stale {
		s.cmd = nil
		job = s.job
		s.job = nil
	}
	s.mu.Unlock()
	if job != nil {
		job.Close()
	}
	if stale {
		return
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	exit := &ipc.CoreExit{At: time.Now(), Code: code, Expected: expected}
	if !expected {
		exit.LogTail = s.opt.CoreLog.Tail(20)
	}
	s.opt.Log.Line("核心退出：code %d%s", code, map[bool]string{true: "，预期内", false: "，非预期"}[expected])
	s.opt.Platform.AfterCoreStopped()

	s.mu.Lock()
	s.lastExit = exit
	if !expected {
		// 非预期退出：TUN 网卡与 WFP 规则已随进程消失，网络已经恢复；这里只记录状态。
		s.phase = ipc.PhaseCrashed
		s.port, s.secret, s.tunInterface, s.startedAt = 0, "", "", nil
	}
	s.mu.Unlock()
}

// MARK: 停止

func (s *Supervisor) Stop() ipc.Response {
	s.op.Lock()
	defer s.op.Unlock()
	s.stopLocked()
	st := s.Status()
	return ipc.Response{OK: true, Status: &st}
}

// Shutdown 在服务退出（停止服务、关机）前调用。
func (s *Supervisor) Shutdown() {
	s.op.Lock()
	defer s.op.Unlock()
	s.stopLocked()
}

func (s *Supervisor) stopLocked() {
	s.mu.Lock()
	if s.phase == ipc.PhaseRunning || s.phase == ipc.PhaseStarting {
		s.phase = ipc.PhaseStopping
	}
	s.mu.Unlock()
	s.terminateCore()
	s.mu.Lock()
	s.phase = ipc.PhaseStopped
	s.cmd = nil
	s.port, s.secret, s.tunInterface, s.startedAt = 0, "", "", nil
	s.mu.Unlock()
}

// terminateCore 结束当前核心（预期内退出），不改变对外的阶段。
func (s *Supervisor) terminateCore() {
	s.mu.Lock()
	s.expectedExit = true
	cmd := s.cmd
	exited := s.exited
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil && exited != nil {
		terminate(cmd.Process)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			s.opt.Log.Line("核心 5 秒内没有退出，强制结束")
			_ = cmd.Process.Kill()
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
			}
		}
	}
}

// MARK: 订阅刷新

func (s *Supervisor) UpdateSubscription(body []byte) ipc.Response {
	s.op.Lock()
	defer s.op.Unlock()
	if err := subscription.Validate(body); err != nil {
		return ipc.Failure("bad-request", err.Error(), "")
	}
	if err := s.opt.Platform.PrepareDirs(s.opt.System); err != nil {
		return ipc.Failure("write-failed", "准备工作目录失败。", err.Error())
	}
	if err := paths.WriteFileAtomic(filepath.Join(s.opt.System.CoreHome, "providers", "sub.yaml"), body, 0o600); err != nil {
		return ipc.Failure("write-failed", "写入订阅失败。", err.Error())
	}
	st := s.Status()
	return ipc.Response{OK: true, Status: &st}
}

// ResetConnections 在睡眠唤醒后调用：旧网络上建立的连接都已失效，清掉让应用按新网络重连。
func (s *Supervisor) ResetConnections() {
	st := s.Status()
	if st.Phase != ipc.PhaseRunning {
		return
	}
	s.mu.Lock()
	port, secret := s.port, s.secret
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mihomo.New(port, secret).CloseAllConnections(ctx); err == nil {
		s.opt.Log.Line("唤醒后已清理旧连接")
	}
}

// MARK: 就绪检查

func (s *Supervisor) isCurrent(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return gen == s.generation && s.cmd != nil
}

// waitController 等控制接口应答。portTaken 表示核心日志里报了控制端口被占用。
func (s *Supervisor) waitController(port int, secret string, gen int, timeout time.Duration) (ready, portTaken bool) {
	api := mihomo.New(port, secret)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.isCurrent(gen) {
			return false, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := api.Version(ctx)
		cancel()
		if err == nil {
			return true, false
		}
		if s.currentLaunchLogContains("External controller listen error") {
			return false, true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false, false
}

// currentLaunchLogContains 只看本次启动之后的核心日志。
func (s *Supervisor) currentLaunchLogContains(needle string) bool {
	tail := s.opt.CoreLog.Tail(60)
	if i := strings.LastIndex(tail, launchMarker); i >= 0 {
		tail = tail[i:]
	}
	return strings.Contains(tail, needle)
}

func (s *Supervisor) waitInterface(address string, gen int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.isCurrent(gen) {
			return ""
		}
		if name := InterfaceWithIPv4(address); name != "" {
			return name
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// InterfaceWithIPv4 找到持有某个 IPv4 地址的网卡（内核层面的证据）。
func InterfaceWithIPv4(target string) string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && ipnet.IP.String() == target {
				return iface.Name
			}
		}
	}
	return ""
}

// MARK: 文件

// syncGeo 把安装目录里的地理库复制到核心工作目录（大小或时间不同才复制）。
func (s *Supervisor) syncGeo() error {
	for _, src := range []string{s.opt.Components.GeoIP(), s.opt.Components.GeoSite()} {
		dst := filepath.Join(s.opt.System.CoreHome, filepath.Base(src))
		si, err := os.Stat(src)
		if err != nil {
			return err
		}
		if di, err := os.Stat(dst); err == nil && di.Size() == si.Size() && !si.ModTime().After(di.ModTime()) {
			continue
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(in)
		in.Close()
		if err != nil {
			return err
		}
		if err := paths.WriteFileAtomic(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// MARK: 参数校验

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validate 逐项检查 App 发来的参数，返回可以直接用来生成配置的选项。
func validate(p ipc.StartParams) (config.Options, error) {
	var o config.Options
	if err := subscription.Validate(p.Subscription); err != nil {
		return o, fmt.Errorf("订阅内容：%w", err)
	}
	for _, rule := range p.CustomRules {
		if strings.ContainsAny(rule, "\r\n") {
			return o, errors.New("自定义规则里有换行")
		}
	}
	rules, err := config.ParseCustomRules(strings.Join(p.CustomRules, "\n"))
	if err != nil {
		return o, fmt.Errorf("自定义规则：%w", err)
	}
	if len(rules) != len(p.CustomRules) {
		return o, errors.New("自定义规则里有空行或注释")
	}
	o.CustomRules = rules
	if len(p.DirectHosts) > 8 {
		return o, errors.New("直连主机太多")
	}
	for _, host := range p.DirectHosts {
		if _, err := netip.ParseAddr(host); err != nil && !hostnamePattern.MatchString(host) {
			return o, fmt.Errorf("主机名无效：%q", host)
		}
		o.DirectHosts = append(o.DirectHosts, host)
	}
	if p.HealthCheckURL != "" {
		u, err := url.Parse(p.HealthCheckURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			len(p.HealthCheckURL) > 512 || strings.ContainsAny(p.HealthCheckURL, " \"'\\\r\n\t") {
			return o, fmt.Errorf("测速地址无效：%q", p.HealthCheckURL)
		}
		o.HealthCheckURL = p.HealthCheckURL
	}
	if len(p.LocalDNS) > 4 {
		return o, errors.New("本地 DNS 太多")
	}
	for _, server := range p.LocalDNS {
		addr, err := netip.ParseAddr(server)
		if err != nil || !addr.Is4() {
			return o, fmt.Errorf("本地 DNS 无效：%q", server)
		}
		o.LocalDNS = append(o.LocalDNS, addr.String())
	}
	o.StrictRoute = p.StrictRoute
	o.TailscaleDNS = p.TailscaleDNS
	return o, nil
}

const launchMarker = "---- 启动核心 ----"

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
