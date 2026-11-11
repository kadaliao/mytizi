// Package mihomo 是 mihomo 控制接口（external-controller）的最小客户端，只用到 App 需要的那几个接口。
package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError 是控制接口返回的非 2xx。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("控制接口返回 %d：%s", e.Status, e.Body)
}

// TransportError 表示控制接口本身连不上。
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "无法连接控制接口：" + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

type DelayHistory struct {
	Time  string `json:"time"`
	Delay int    `json:"delay"`
}

type ProviderProxy struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	UDP     bool           `json:"udp"`
	History []DelayHistory `json:"history"`
}

// LastDelay 是最近一次测速的延迟（毫秒）；0 表示不可用/未测。
func (p ProviderProxy) LastDelay() int {
	if len(p.History) == 0 {
		return 0
	}
	if d := p.History[len(p.History)-1].Delay; d > 0 {
		return d
	}
	return 0
}

type Provider struct {
	Name    string          `json:"name"`
	Proxies []ProviderProxy `json:"proxies"`
}

type Group struct {
	Name string   `json:"name"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

type ConnectionMetadata struct {
	Host            string `json:"host"`
	DestinationIP   string `json:"destinationIP"`
	DestinationPort string `json:"destinationPort"`
	Network         string `json:"network"`
	Type            string `json:"type"`
	Process         string `json:"process"`
}

type Connection struct {
	ID          string             `json:"id"`
	Metadata    ConnectionMetadata `json:"metadata"`
	Chains      []string           `json:"chains"`
	Rule        string             `json:"rule"`
	RulePayload string             `json:"rulePayload"`
}

type Connections struct {
	UploadTotal   int64        `json:"uploadTotal"`
	DownloadTotal int64        `json:"downloadTotal"`
	Connections   []Connection `json:"connections"`
}

// Client 访问 127.0.0.1 上的控制接口。回环请求绝不走系统代理。
type Client struct {
	Port   int
	Secret string
	http   *http.Client
}

func New(port int, secret string) *Client {
	return &Client{
		Port: port, Secret: secret,
		http: &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 30 * time.Second},
	}
}

// Version 返回核心版本。
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	err := c.send(ctx, "GET", "/version", nil, nil, &v, 3*time.Second)
	return v.Version, err
}

func (c *Client) Provider(ctx context.Context, name string) (*Provider, error) {
	var p Provider
	if err := c.send(ctx, "GET", "/providers/proxies/"+name, nil, nil, &p, 5*time.Second); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) UpdateProvider(ctx context.Context, name string) error {
	return c.send(ctx, "PUT", "/providers/proxies/"+name, nil, nil, nil, 15*time.Second)
}

func (c *Client) Group(ctx context.Context, name string) (*Group, error) {
	var g Group
	if err := c.send(ctx, "GET", "/proxies/"+name, nil, nil, &g, 5*time.Second); err != nil {
		return nil, err
	}
	return &g, nil
}

func (c *Client) Select(ctx context.Context, group, proxy string) error {
	return c.send(ctx, "PUT", "/proxies/"+group, nil, map[string]string{"name": proxy}, nil, 5*time.Second)
}

// GroupDelay 整组并发测速，返回 节点名 → 毫秒（失败的节点不在结果里）。
func (c *Client) GroupDelay(ctx context.Context, group, testURL string, timeoutMs int) (map[string]int, error) {
	q := url.Values{"url": {testURL}, "timeout": {fmt.Sprint(timeoutMs)}}
	result := map[string]int{}
	err := c.send(ctx, "GET", "/group/"+group+"/delay", q, nil, &result, time.Duration(timeoutMs)*time.Millisecond+5*time.Second)
	return result, err
}

func (c *Client) Connections(ctx context.Context) (*Connections, error) {
	var s Connections
	if err := c.send(ctx, "GET", "/connections", nil, nil, &s, 5*time.Second); err != nil {
		return nil, err
	}
	return &s, nil
}

// CloseAllConnections：网络切换/唤醒后清掉走旧出口的连接，让应用按新的网络重连。
func (c *Client) CloseAllConnections(ctx context.Context) error {
	return c.send(ctx, "DELETE", "/connections", nil, nil, nil, 3*time.Second)
}

// endpoint 拼地址。节点名常含中文、空格和 emoji，必须按路径段编码。
func (c *Client) endpoint(p string, q url.Values) string {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/%s", c.Port, strings.Join(segments, "/"))
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) send(ctx context.Context, method, p string, q url.Values, body any, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(p, q), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &TransportError{Err: err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := string(data)
		if len(text) > 300 {
			text = text[:300]
		}
		return &HTTPError{Status: resp.StatusCode, Body: text}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("控制接口返回了无法识别的内容：%w", err)
	}
	return nil
}
