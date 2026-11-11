package appcore

import "time"

// UIState 是界面渲染所需的全部数据。每次变化都会通过 "state" 事件整份推给前端，
// 前端也可以随时调用 App.State() 主动拉取。字段名就是前端读取的 JSON 键。
type UIState struct {
	// Phase：needsSubscription | disconnected | connecting | connected | disconnecting | failed
	Phase string `json:"phase"`
	// Step 是 connecting 阶段的进度文字（例如"正在启动…"）。
	Step    string `json:"step"`
	Version string `json:"version"` // "0.4.0" 或 "dev"

	Subscription *SubscriptionView `json:"subscription"` // 没有订阅时为 null
	Nodes        []NodeRow         `json:"nodes"`
	// Selection 是 "AUTO"（自动选择）或某个节点名。
	Selection  string     `json:"selection"`
	ActiveNode string     `json:"activeNode"`
	StartedAt  *time.Time `json:"startedAt"` // 已连接时的开始时间（RFC3339），前端自己算"已运行 x 分钟"

	ErrorMessage      string `json:"errorMessage"`      // 失败卡片的文字
	Notice            string `json:"notice"`            // 底部的短暂提示（几秒后自动清空）
	ConnectivityIssue string `json:"connectivityIssue"` // 已连接但所有线路都不通
	// ServiceProblem 非空表示后台组件没装好/没运行，界面应提供"修复后台组件"按钮（调用 RepairService）。
	ServiceProblem string `json:"serviceProblem"`

	AddingSubscription     bool   `json:"addingSubscription"`
	AddProgress            string `json:"addProgress"`
	AddError               string `json:"addError"`
	RefreshingSubscription bool   `json:"refreshingSubscription"`
	TestingLatency         bool   `json:"testingLatency"`
	CopyingDiagnostics     bool   `json:"copyingDiagnostics"`
	CheckingUpdate         bool   `json:"checkingUpdate"`
	RepairingService       bool   `json:"repairingService"`

	AvailableUpdate *UpdateView  `json:"availableUpdate"` // 有新版本时非空
	Settings        SettingsView `json:"settings"`
}

type SubscriptionView struct {
	Name        string     `json:"name"`
	Host        string     `json:"host"`
	NodeCount   int        `json:"nodeCount"`
	UpdatedText string     `json:"updatedText"` // "3 小时前更新" / "尚未更新"
	Usage       *UsageView `json:"usage"`       // 服务端没有给流量信息时为 null
}

type UsageView struct {
	Fraction   float64 `json:"fraction"`   // 0...1，> 0.9 显示红色
	UsedText   string  `json:"usedText"`   // "40.05 GB"
	TotalText  string  `json:"totalText"`  // "186.26 GB"
	ExpireText string  `json:"expireText"` // "2027年1月18日"，不过期时为空
}

type NodeRow struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Delay int    `json:"delay"` // 毫秒；0 表示没有测速结果
}

type UpdateView struct {
	Version string `json:"version"`
}

type SettingsView struct {
	StrictRoute         bool `json:"strictRoute"`
	AutoConnectOnLaunch bool `json:"autoConnectOnLaunch"`
	AutoCheckUpdates    bool `json:"autoCheckUpdates"`
	LaunchAtLogin       bool `json:"launchAtLogin"`
}

// AutoSelection 是"自动选择（延迟最低）"在 Selection 里的取值。
const AutoSelection = "AUTO"
