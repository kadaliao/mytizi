//go:build !windows

package diag

import (
	"context"
	"net"
	"runtime"
	"time"
)

// CollectFacts 在非 Windows 平台（开发）上只列出网卡与地址。
func CollectFacts(_ context.Context, _ int) Facts {
	facts := Facts{OS: runtime.GOOS + "（开发环境）", Arch: runtime.GOARCH, Timestamp: time.Now()}
	interfaces, err := net.Interfaces()
	if err != nil {
		return facts
	}
	for _, iface := range interfaces {
		adapter := Adapter{Name: iface.Name, Up: iface.Flags&net.FlagUp != 0}
		if iface.Flags&net.FlagLoopback != 0 {
			adapter.IfType = IfTypeLoopback
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if ipnet.IP.To4() != nil {
					adapter.IPv4 = append(adapter.IPv4, ipnet.IP.String())
				} else {
					adapter.IPv6 = append(adapter.IPv6, ipnet.IP.String())
				}
			}
		}
		facts.Adapters = append(facts.Adapters, adapter)
	}
	return facts
}
