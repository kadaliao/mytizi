// Package appinfo 放全局常量。改名只需要改这里和 scripts/build.sh 顶部的变量。
package appinfo

const (
	DisplayName = "MyTizi"
	// Repository 是 GitHub 仓库（owner/name），用于检查更新。
	Repository = "kadaliao/mytizi"
	ProjectURL = "https://github.com/" + Repository

	// ServiceName 是后台服务在 Windows 服务管理器里的名字。
	ServiceName        = "MyTiziHelper"
	ServiceDisplayName = "MyTizi 后台组件"
	ServiceDescription = "为 MyTizi 启动内置的 mihomo 核心并创建虚拟网卡（TUN）。没有连接时空闲。"

	// PipeName 是 App 与后台服务之间的命名管道。
	PipeName = `\\.\pipe\MyTizi.Helper`

	// IPCVersion 是 App 与后台服务之间的协议版本。不兼容变化时递增，App 会提示重新安装。
	IPCVersion = 1

	// TunGateway 是内置核心使用的 TUN 地址。.1 给 mihomo，.2 是 TUN 网卡上设置的 DNS（不会被 fake-ip 池分配）。
	TunGateway = "198.18.0.1"
	TunDNS     = "198.18.0.2"
	// TunDevice 是虚拟网卡的名字，会出现在「网络连接」里。
	TunDevice = "MyTizi"
)

// Version 由构建脚本通过 -ldflags "-X .../appinfo.Version=x.y.z" 写入；开发构建是 "dev"。
var Version = "dev"
