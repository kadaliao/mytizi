//go:build windows

package diag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/kadaliao/mytizi/windows/internal/proc"
)

// CollectFacts 采集一份快照。excludePID 是本 App 核心的进程号（不算冲突）。
func CollectFacts(ctx context.Context, excludePID int) Facts {
	facts := Facts{OS: windowsVersion(), Arch: archDescription(), Timestamp: time.Now()}
	var wg sync.WaitGroup
	var routeText string
	var nrptText string
	var nrptErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		routeText, _ = run(ctx, 5*time.Second, "route.exe", "print", "-4")
	}()
	go func() {
		defer wg.Done()
		nrptText, nrptErr = run(ctx, 8*time.Second, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"[Console]::OutputEncoding=[Text.Encoding]::UTF8; "+
				"Get-DnsClientNrptPolicy | Select-Object Namespace,NameServers | ConvertTo-Json -Compress")
	}()

	if adapters, err := adapterList(); err == nil {
		facts.Adapters = adapters
	}
	facts.Proxy = systemProxy()
	processes := processList()
	exclude := map[int]bool{}
	if excludePID > 0 {
		exclude[excludePID] = true
	}
	facts.OtherProxyProcesses = ConflictingProcesses(processes, exclude)
	for _, p := range processes {
		if IsTailscaleProcess(p.Name) {
			facts.TailscaleRunning = true
		}
	}

	wg.Wait()
	facts.DefaultRoutes = AttachRouteAdapters(ParseRoutePrint(routeText), facts.Adapters)
	if nrptErr != nil {
		facts.NRPTError = nrptErr.Error()
	} else if rules, err := ParseNRPTJSON(nrptText); err != nil {
		facts.NRPTError = "无法解析：" + err.Error()
	} else {
		facts.NRPT = rules
	}
	return facts
}

func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return string(out), fmt.Errorf("%v：%s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return string(out), err
	}
	return string(out), nil
}

// windowsVersion：Windows 11 的注册表里 ProductName 仍然写着 Windows 10，按内部版本号纠正。
func windowsVersion() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer key.Close()
	product, _, _ := key.GetStringValue("ProductName")
	display, _, _ := key.GetStringValue("DisplayVersion")
	build, _, _ := key.GetStringValue("CurrentBuildNumber")
	ubr, _, _ := key.GetIntegerValue("UBR")
	var number int
	fmt.Sscanf(build, "%d", &number)
	if number >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	text := strings.TrimSpace(product + " " + display)
	if build != "" {
		text += fmt.Sprintf("（%s.%d）", build, ubr)
	}
	return text
}

// archDescription：程序架构与系统架构不同（例如 ARM 电脑上运行 x64 版）时一并写出。
func archDescription() string {
	system := os.Getenv("PROCESSOR_ARCHITEW6432")
	if system == "" {
		system = os.Getenv("PROCESSOR_ARCHITECTURE")
	}
	system = strings.ToLower(system)
	program := runtime.GOARCH
	if system == "" || system == program || (system == "amd64" && program == "amd64") {
		return program
	}
	return fmt.Sprintf("%s 程序，系统 %s", program, system)
}

func adapterList() ([]Adapter, error) {
	size := uint32(16 * 1024)
	for attempt := 0; attempt < 4; attempt++ {
		buffer := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0, first, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var out []Adapter
		for a := first; a != nil; a = a.Next {
			adapter := Adapter{
				Name:        windows.UTF16PtrToString(a.FriendlyName),
				Description: windows.UTF16PtrToString(a.Description),
				IfType:      a.IfType,
				Up:          a.OperStatus == windows.IfOperStatusUp,
				Metric:      a.Ipv4Metric,
			}
			for u := a.FirstUnicastAddress; u != nil; u = u.Next {
				appendIP(u.Address.IP(), &adapter.IPv4, &adapter.IPv6)
			}
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				appendIP(d.Address.IP(), &adapter.DNS, &adapter.DNS)
			}
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				if ip := g.Address.IP(); ip != nil && ip.To4() != nil {
					adapter.Gateways = append(adapter.Gateways, ip.String())
				}
			}
			runtime.KeepAlive(buffer)
			out = append(out, adapter)
		}
		return out, nil
	}
	return nil, errors.New("GetAdaptersAddresses：缓冲区一直不够")
}

func appendIP(ip net.IP, v4, v6 *[]string) {
	if ip == nil {
		return
	}
	if ip.To4() != nil {
		*v4 = append(*v4, ip.To4().String())
	} else {
		*v6 = append(*v6, ip.String())
	}
}

// systemProxy 读当前用户的 Internet 选项；指向本机却没人监听的地址记为失效。
func systemProxy() ProxyInfo {
	var info ProxyInfo
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return info
	}
	defer key.Close()
	enabled, _, _ := key.GetIntegerValue("ProxyEnable")
	info.Enabled = enabled != 0
	info.Server, _, _ = key.GetStringValue("ProxyServer")
	info.AutoConfigURL, _, _ = key.GetStringValue("AutoConfigURL")
	if info.Enabled {
		for _, endpoint := range ProxyEndpoints(info.Server) {
			if !IsLoopbackEndpoint(endpoint) {
				continue
			}
			conn, err := net.DialTimeout("tcp", endpoint, 300*time.Millisecond)
			if err != nil {
				info.DeadEndpoints = append(info.DeadEndpoints, endpoint)
				continue
			}
			conn.Close()
		}
	}
	return info
}

func processList() []Process {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []Process
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		out = append(out, Process{PID: int(entry.ProcessID), Name: windows.UTF16ToString(entry.ExeFile[:])})
	}
	return out
}
