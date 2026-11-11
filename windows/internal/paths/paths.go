// Package paths 集中所有文件位置。
//
//	安装目录（管理员可写）  C:\Program Files\MyTizi\{MyTizi.exe, mytizi-service.exe, core\mihomo.exe, core\geo*}
//	系统数据（服务写）      C:\ProgramData\MyTizi\{core-home\, logs\helper.log, logs\mihomo.log}
//	用户数据                %APPDATA%\MyTizi\{settings.json, subscription.txt, custom-rules.txt, last-config.redacted.yaml}
//	用户日志与缓存          %LOCALAPPDATA%\MyTizi\{Logs\app.log, Cache\}
package paths

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/kadaliao/mytizi/windows/internal/appinfo"
)

// System 是后台服务拥有的位置：服务以 SYSTEM 身份读写，App 只读日志。
type System struct {
	Base     string
	CoreHome string
	LogDir   string
}

// DefaultSystem 返回 %ProgramData%\MyTizi 下的位置。
func DefaultSystem() System {
	base := os.Getenv("ProgramData")
	if base == "" || runtime.GOOS != "windows" {
		base = filepath.Join(os.TempDir(), "mytizi-programdata")
	}
	return SystemAt(filepath.Join(base, appinfo.DisplayName))
}

// SystemAt 用于测试：把系统位置放到任意目录。
func SystemAt(base string) System {
	return System{Base: base, CoreHome: filepath.Join(base, "core-home"), LogDir: filepath.Join(base, "logs")}
}

func (s System) HelperLog() string  { return filepath.Join(s.LogDir, "helper.log") }
func (s System) CoreLog() string    { return filepath.Join(s.LogDir, "mihomo.log") }
func (s System) ConfigPath() string { return filepath.Join(s.CoreHome, "config.yaml") }

// InstallDir 是当前可执行文件所在的目录（App 与服务装在一起）。
func InstallDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// Components 是随安装包分发的核心与地理库。
type Components struct{ Dir string }

func ComponentsIn(installDir string) Components {
	return Components{Dir: filepath.Join(installDir, "core")}
}

func (c Components) Core() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(c.Dir, "mihomo.exe")
	}
	return filepath.Join(c.Dir, "mihomo")
}
func (c Components) GeoIP() string   { return filepath.Join(c.Dir, "geoip.metadb") }
func (c Components) GeoSite() string { return filepath.Join(c.Dir, "geosite.dat") }

// User 是当前用户目录下的位置。
type User struct {
	Support string
	Logs    string
	Cache   string
}

// DefaultUser 返回 %APPDATA% 与 %LOCALAPPDATA% 下的位置。
func DefaultUser() User {
	config, err := os.UserConfigDir()
	if err != nil {
		config = os.TempDir()
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	name := appinfo.DisplayName
	if runtime.GOOS != "windows" {
		// 在 macOS 上开发调试时绝不能碰 macOS 版 MyTizi 的 ~/Library/Application Support/MyTizi。
		name = appinfo.DisplayName + " Windows Dev"
	}
	return User{
		Support: filepath.Join(config, name),
		Logs:    filepath.Join(cache, name, "Logs"),
		Cache:   filepath.Join(cache, name, "Cache"),
	}
}

// UserAt 用于测试。
func UserAt(home string) User {
	return User{
		Support: filepath.Join(home, "support"),
		Logs:    filepath.Join(home, "logs"),
		Cache:   filepath.Join(home, "cache"),
	}
}

func (u User) Settings() string         { return filepath.Join(u.Support, "settings.json") }
func (u User) SubscriptionBody() string { return filepath.Join(u.Support, "subscription.txt") }
func (u User) CustomRules() string      { return filepath.Join(u.Support, "custom-rules.txt") }
func (u User) LastConfigRedacted() string {
	return filepath.Join(u.Support, "last-config.redacted.yaml")
}
func (u User) AppLog() string { return filepath.Join(u.Logs, "app.log") }

// Prepare 创建用户目录。
func (u User) Prepare() error {
	for _, dir := range []string{u.Support, u.Logs, u.Cache} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// WriteFileAtomic 先写同目录临时文件再改名，避免断电或崩溃留下半截文件。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	_ = os.Chmod(name, perm)
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
