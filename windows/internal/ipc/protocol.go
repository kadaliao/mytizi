// Package ipc 是 App ⇄ 后台服务的协议：命名管道上的按行 JSON，一次连接一问一答。
//
// 与 macOS 版的差别：App 不再把整份 YAML 交给后台，只交"订阅原文 + 少量设置"，配置由服务自己生成。
// 服务以 SYSTEM 身份运行核心，可配置的东西越少，能被别的程序借道利用的就越少。
package ipc

import "time"

type Method string

const (
	MethodHello  Method = "hello"
	MethodStatus Method = "status"
	MethodStart  Method = "start"
	MethodStop   Method = "stop"
	// MethodUpdateSubscription 只替换订阅文件（订阅刷新），不重启核心。
	MethodUpdateSubscription Method = "updateSubscription"
)

// StartParams 是连接所需的全部输入。服务会逐项校验后再生成配置。
type StartParams struct {
	// Subscription 是订阅原文（任意格式，由 mihomo 解析）。
	Subscription []byte `json:"subscription"`
	// CustomRules 会在服务里按同样的规则重新解析一遍。
	CustomRules []string `json:"customRules,omitempty"`
	// DirectHosts 是必须直连的主机名（订阅所在主机）。
	DirectHosts    []string `json:"directHosts,omitempty"`
	HealthCheckURL string   `json:"healthCheckURL,omitempty"`
	// LocalDNS 是连接前系统使用的 IPv4 DNS（路由器），只用于 .lan 之类的本地域名。
	LocalDNS     []string `json:"localDNS,omitempty"`
	StrictRoute  bool     `json:"strictRoute"`
	TailscaleDNS bool     `json:"tailscaleDNS"`
}

type Request struct {
	Method       Method       `json:"method"`
	Start        *StartParams `json:"start,omitempty"`
	Subscription []byte       `json:"subscription,omitempty"`
}

// Error 是服务返回的错误。Detail 是给排查用的原始信息（核心日志尾部等），不直接展示给用户。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type Hello struct {
	IPCVersion     int    `json:"ipcVersion"`
	ServicePID     int    `json:"servicePID"`
	ServiceVersion string `json:"serviceVersion"`
	CoreInstalled  bool   `json:"coreInstalled"`
	// InstallDir 是服务所在的目录（核心也在这里）。
	InstallDir string `json:"installDir"`
}

type CoreExit struct {
	At       time.Time `json:"at"`
	Code     int       `json:"code"`
	Expected bool      `json:"expected"`
	LogTail  string    `json:"logTail,omitempty"`
}

type Phase string

const (
	PhaseStopped  Phase = "stopped"
	PhaseStarting Phase = "starting"
	PhaseRunning  Phase = "running"
	PhaseStopping Phase = "stopping"
	PhaseCrashed  Phase = "crashed"
)

type Status struct {
	Phase          Phase      `json:"phase"`
	CorePID        int        `json:"corePID,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	ControllerPort int        `json:"controllerPort,omitempty"`
	// ControllerSecret 仅在 running 时返回：App 重新打开后据此重新接上控制接口。
	ControllerSecret string    `json:"controllerSecret,omitempty"`
	TunInterface     string    `json:"tunInterface,omitempty"`
	StrictRoute      bool      `json:"strictRoute"`
	LastExit         *CoreExit `json:"lastExit,omitempty"`
}

type Response struct {
	OK     bool    `json:"ok"`
	Error  *Error  `json:"error,omitempty"`
	Hello  *Hello  `json:"hello,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// Failure 构造一个失败响应。
func Failure(code, message, detail string) Response {
	return Response{OK: false, Error: &Error{Code: code, Message: message, Detail: detail}}
}
