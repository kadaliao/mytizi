// Package settings 是 App 的设置与订阅记录（保存在 %APPDATA%\MyTizi\settings.json）。
package settings

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/paths"
)

// Node 是订阅里解析出的一条线路。
type Node struct {
	Name string `json:"name"`
	// Type 是 mihomo 报告的类型，例如 Shadowsocks、Vmess、Trojan、Http。
	Type string `json:"type"`
	UDP  bool   `json:"udp"`
}

// Usage 来自订阅响应头 subscription-userinfo。
type Usage struct {
	Upload   int64      `json:"upload"`
	Download int64      `json:"download"`
	Total    int64      `json:"total"`
	Expire   *time.Time `json:"expire,omitempty"`
}

func (u Usage) Used() int64 { return u.Upload + u.Download }

// Subscription 是唯一的订阅（产品只支持一个订阅）。
type Subscription struct {
	URL       string     `json:"url"`
	Name      string     `json:"name"`
	AddedAt   time.Time  `json:"addedAt"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Nodes     []Node     `json:"nodes"`
	Usage     *Usage     `json:"usage,omitempty"`
	// UpdateIntervalHours 是服务端通过 profile-update-interval 建议的刷新间隔。
	UpdateIntervalHours int `json:"updateIntervalHours,omitempty"`
}

// Host 返回订阅链接的主机名。
func (s Subscription) Host() string {
	u, err := url.Parse(s.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Settings 是全部设置。新增字段时给出默认值即可：旧的 settings.json 里缺的字段保持默认。
type Settings struct {
	Subscription *Subscription `json:"subscription,omitempty"`
	// StrictRoute：连接期间拦截其他网卡上的 DNS 与全部 IPv6（mihomo tun.strict-route）。
	// 默认开启：不拦的话 Windows 会同时向所有网卡的 DNS 发查询，IPv6 流量也会绕过规则。
	StrictRoute          bool       `json:"strictRoute"`
	HealthCheckURL       string     `json:"healthCheckURL"`
	AutoConnectOnLaunch  bool       `json:"autoConnectOnLaunch"`
	AutoCheckUpdates     bool       `json:"autoCheckUpdates"`
	LastUpdateCheck      *time.Time `json:"lastUpdateCheck,omitempty"`
	SkippedUpdateVersion string     `json:"skippedUpdateVersion,omitempty"`
}

// Default 返回默认设置。
func Default() Settings {
	return Settings{StrictRoute: true, HealthCheckURL: config.DefaultHealthCheckURL, AutoCheckUpdates: true}
}

// Store 负责读写设置、订阅原文与自定义规则。
type Store struct{ Paths paths.User }

func NewStore(p paths.User) Store { return Store{Paths: p} }

// Load 读取设置；文件缺失或损坏时返回默认值。
func (s Store) Load() Settings {
	result := Default()
	data, err := os.ReadFile(s.Paths.Settings())
	if err != nil {
		return result
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return Default()
	}
	if result.HealthCheckURL == "" {
		result.HealthCheckURL = config.DefaultHealthCheckURL
	}
	return result
}

func (s Store) Save(v Settings) error {
	if err := s.Paths.Prepare(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(s.Paths.Settings(), data, 0o600)
}

func (s Store) LoadSubscriptionBody() ([]byte, error) { return os.ReadFile(s.Paths.SubscriptionBody()) }

func (s Store) SaveSubscriptionBody(body []byte) error {
	if err := s.Paths.Prepare(); err != nil {
		return err
	}
	return paths.WriteFileAtomic(s.Paths.SubscriptionBody(), body, 0o600)
}

// LoadCustomRules 读取并校验自定义规则；文件不存在时返回空。
func (s Store) LoadCustomRules() ([]string, error) {
	data, err := os.ReadFile(s.Paths.CustomRules())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return config.ParseCustomRules(string(data))
}
