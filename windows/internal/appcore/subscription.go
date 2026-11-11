package appcore

import (
	"context"
	"errors"
	"time"

	"github.com/kadaliao/mytizi/windows/internal/config"
	"github.com/kadaliao/mytizi/windows/internal/redact"
	"github.com/kadaliao/mytizi/windows/internal/settings"
	"github.com/kadaliao/mytizi/windows/internal/subscription"
)

// MARK: 订阅

// AddSubscription 下载、用核心解析、保存。全部成功才替换现有订阅。
func (c *Core) AddSubscription(input string) {
	proceed := false
	c.change(func() {
		if !c.adding {
			c.adding = true
			c.addError = ""
			proceed = true
		}
	})
	if !proceed {
		return
	}
	defer c.change(func() {
		c.adding = false
		c.addProgress = ""
	})
	err := c.addSubscription(input)
	if err != nil {
		c.change(func() { c.addError = Message(err) })
		r := redact.New("")
		r.AddSubscriptionURL(input)
		c.logRedacted(r, "error", "添加订阅失败：%v", err)
	}
}

func (c *Core) addSubscription(input string) error {
	u, err := subscription.NormalizeURL(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c.change(func() { c.addProgress = "正在下载订阅…" })
	fetched, err := c.d.Fetch(ctx, u.String())
	if err != nil {
		return err
	}
	c.change(func() { c.addProgress = "正在解析线路…" })
	nodes, err := c.probe(ctx, fetched.Body)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return subscription.ErrNoNodes
	}
	if err := c.d.Store.SaveSubscriptionBody(fetched.Body); err != nil {
		return err
	}
	now := time.Now()
	name := fetched.Title
	if name == "" {
		name = u.Hostname()
	}
	record := &settings.Subscription{
		URL: u.String(), Name: name, AddedAt: now, UpdatedAt: &now, Nodes: nodes,
		Usage: fetched.Usage, UpdateIntervalHours: fetched.UpdateIntervalHours,
	}
	var saveErr error
	c.change(func() {
		next := c.settings
		next.Subscription = record
		if saveErr = c.d.Store.Save(next); saveErr != nil {
			return
		}
		c.settings = next
		c.nodes = rowsFrom(nodes)
		c.selection = AutoSelection
		if !c.isBusyLocked() && c.phase != PhaseConnected {
			c.phase = PhaseDisconnected
		}
		c.errorMessage = ""
	})
	if saveErr != nil {
		return saveErr
	}
	c.logf("info", "已添加订阅 %s，%d 条线路", u.Hostname(), len(nodes))
	return nil
}

func (c *Core) probe(ctx context.Context, body []byte) ([]settings.Node, error) {
	nodes, err := c.d.Probe(ctx, body)
	if err != nil && !errors.Is(err, context.Canceled) {
		var se *subscription.Error
		if errors.As(err, &se) {
			return nil, err
		}
		return nil, subscription.NotASubscription("没能从订阅里解析出线路：" + err.Error())
	}
	return nodes, err
}

// RefreshSubscription 重新下载订阅。订阅主机在规则里固定直连，所以已连接时也不需要（也不应该）走代理。
func (c *Core) RefreshSubscription() { c.refreshSubscription(true) }

func (c *Core) refreshSubscription(manual bool) {
	var record settings.Subscription
	proceed := false
	c.change(func() {
		if c.refreshing || c.settings.Subscription == nil {
			return
		}
		c.refreshing = true
		record = *c.settings.Subscription
		proceed = true
	})
	if !proceed {
		return
	}
	defer c.change(func() { c.refreshing = false })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	count, err := c.doRefresh(ctx, record)
	c.mu.Lock()
	c.lastSubscriptionCheck = time.Now()
	c.mu.Unlock()
	if err != nil {
		c.logf("error", "刷新订阅失败：%v", err)
		if manual {
			c.ShowNotice("更新失败："+Message(err), 6*time.Second)
		}
		return
	}
	c.logf("info", "订阅已更新，%d 条线路", count)
	if manual {
		c.ShowNotice("订阅已更新："+itoa(count)+" 条线路", 3*time.Second)
	}
}

func (c *Core) doRefresh(ctx context.Context, record settings.Subscription) (int, error) {
	fetched, err := c.d.Fetch(ctx, record.URL)
	if err != nil {
		return 0, err
	}
	nodes, err := c.probe(ctx, fetched.Body)
	if err != nil {
		return 0, err
	}
	if len(nodes) == 0 {
		return 0, subscription.ErrNoNodes
	}
	if err := c.d.Store.SaveSubscriptionBody(fetched.Body); err != nil {
		return 0, err
	}
	now := time.Now()
	record.Nodes = nodes
	record.UpdatedAt = &now
	if fetched.Usage != nil {
		record.Usage = fetched.Usage
	}
	if fetched.UpdateIntervalHours > 0 {
		record.UpdateIntervalHours = fetched.UpdateIntervalHours
	}
	if fetched.Title != "" {
		record.Name = fetched.Title
	}
	var connected bool
	var saveErr error
	c.change(func() {
		next := c.settings
		next.Subscription = &record
		if saveErr = c.d.Store.Save(next); saveErr != nil {
			return
		}
		c.settings = next
		c.nodes = rowsFrom(nodes)
		c.lastDelayRefresh = time.Time{}
		connected = c.phase == PhaseConnected
	})
	if saveErr != nil {
		return 0, saveErr
	}
	if connected {
		if err := c.d.Client.UpdateSubscription(ctx, fetched.Body); err != nil {
			return 0, err
		}
		c.mu.Lock()
		api := c.api
		c.mu.Unlock()
		if api != nil {
			_ = api.UpdateProvider(ctx, config.ProviderName)
		}
	}
	return len(nodes), nil
}

func (c *Core) refreshSubscriptionIfDue() {
	c.mu.Lock()
	sub := c.settings.Subscription
	if sub == nil {
		c.mu.Unlock()
		return
	}
	hours := sub.UpdateIntervalHours
	if hours <= 0 {
		hours = 24
	}
	if hours < 6 {
		hours = 6
	}
	updated := sub.AddedAt
	if sub.UpdatedAt != nil {
		updated = *sub.UpdatedAt
	}
	due := time.Since(updated) > time.Duration(hours)*time.Hour && time.Since(c.lastSubscriptionCheck) > 30*time.Minute
	if due {
		c.lastSubscriptionCheck = time.Now()
	}
	c.mu.Unlock()
	if due {
		go c.refreshSubscription(false)
	}
}

// RemoveSubscription 断开并删除订阅（"更换订阅"）。
func (c *Core) RemoveSubscription() {
	c.mu.Lock()
	connected := c.phase == PhaseConnected || c.isBusyLocked() || c.phase == PhaseFailed
	c.mu.Unlock()
	if connected {
		c.Disconnect()
	}
	c.change(func() {
		next := c.settings
		next.Subscription = nil
		_ = c.d.Store.Save(next)
		c.settings = next
		c.nodes = nil
		c.selection = AutoSelection
		c.activeNode = ""
		c.phase = PhaseNeedsSubscription
		c.errorMessage = ""
		c.addError = ""
	})
}

// MARK: 更新

// checkForUpdatesIfDue：打开 App 后或每天一次自动检查；只提示，不自动安装。
func (c *Core) checkForUpdatesIfDue() {
	c.mu.Lock()
	s := c.settings
	recentAttempt := time.Since(c.lastUpdateAttempt) < 20*time.Hour
	busy := c.checkingUpdate
	c.mu.Unlock()
	if !s.AutoCheckUpdates || busy || recentAttempt || !c.releaseBuild() {
		return
	}
	if s.LastUpdateCheck != nil && time.Since(*s.LastUpdateCheck) < 20*time.Hour {
		return
	}
	c.checkForUpdates(false)
}

func (c *Core) releaseBuild() bool {
	return c.d.CheckUpdate != nil && c.d.IsNewer != nil && c.d.Version != "" && c.d.Version != "dev"
}

// CheckUpdates 是手动检查。
func (c *Core) CheckUpdates() { c.checkForUpdates(true) }

func (c *Core) checkForUpdates(manual bool) {
	if !c.releaseBuild() {
		if manual {
			c.ShowNotice("开发构建（"+c.d.Version+"）不参与版本检查", 5*time.Second)
		}
		return
	}
	proceed := false
	c.change(func() {
		if !c.checkingUpdate {
			c.checkingUpdate = true
			c.lastUpdateAttempt = time.Now()
			proceed = true
		}
	})
	if !proceed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	info, err := c.d.CheckUpdate(ctx)
	cancel()
	c.change(func() { c.checkingUpdate = false })
	if err != nil {
		if manual {
			c.ShowNotice(Message(err), 6*time.Second)
		}
		c.logf("error", "检查更新失败：%v", err)
		return
	}
	now := time.Now()
	c.updateSettings(func(s *settings.Settings) { s.LastUpdateCheck = &now })
	if !c.d.IsNewer(info.Version, c.d.Version) {
		c.change(func() { c.availableUpdate = nil })
		if manual {
			c.ShowNotice("已是最新版本（v"+c.d.Version+"）", 3*time.Second)
		}
		return
	}
	var isNewDiscovery, skipped bool
	c.change(func() {
		isNewDiscovery = c.availableUpdate == nil || c.availableUpdate.Version != info.Version
		skipped = info.Version == c.settings.SkippedUpdateVersion
		if manual || !skipped {
			c.availableUpdate = info
		}
	})
	switch {
	case manual:
		c.ShowNotice("发现新版本 v"+info.Version, 6*time.Second)
	case isNewDiscovery && !skipped:
		c.ShowNotice("发现新版本 v"+info.Version+"，点右下角下载", 8*time.Second)
	}
}

func (c *Core) SkipUpdate() {
	c.mu.Lock()
	update := c.availableUpdate
	c.mu.Unlock()
	if update == nil {
		return
	}
	c.updateSettings(func(s *settings.Settings) { s.SkippedUpdateVersion = update.Version })
	c.change(func() { c.availableUpdate = nil })
	c.ShowNotice("已忽略 v"+update.Version+"，可以在高级设置里手动检查", 4*time.Second)
}

func (c *Core) OpenUpdate() {
	c.mu.Lock()
	update := c.availableUpdate
	c.mu.Unlock()
	if update == nil || c.d.OpenURL == nil {
		return
	}
	target := update.DownloadURL
	if target == "" {
		target = update.ReleaseURL
	}
	_ = c.d.OpenURL(target)
}
