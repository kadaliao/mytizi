// Package rotlog 是追加写入、按大小轮转的日志文件。
package rotlog

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Log struct {
	Path     string
	maxBytes int64
	keep     int
	mu       sync.Mutex
	file     *os.File
	size     int64
}

// New 打开（必要时创建）日志文件。maxBytes 默认 5 MB，保留 2 个旧文件。
func New(path string, maxBytes int64, keep int) *Log {
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}
	if keep <= 0 {
		keep = 2
	}
	l := &Log{Path: path, maxBytes: maxBytes, keep: keep}
	l.mu.Lock()
	l.openLocked()
	l.mu.Unlock()
	return l
}

func (l *Log) openLocked() {
	_ = os.MkdirAll(filepath.Dir(l.Path), 0o755)
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		l.file = nil
		return
	}
	l.file = f
	if info, err := f.Stat(); err == nil {
		l.size = info.Size()
	}
}

// Write 实现 io.Writer（核心的输出直接写进来）。
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(p)) > l.maxBytes {
		l.rotateLocked()
	}
	if l.file == nil {
		l.openLocked()
		if l.file == nil {
			return len(p), nil
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return len(p), err
}

// Line 写一行带时间戳的日志。
func (l *Log) Line(format string, args ...any) {
	stamp := time.Now().Format("2006-01-02T15:04:05-07:00")
	_, _ = l.Write([]byte(stamp + " " + fmt.Sprintf(format, args...) + "\n"))
}

func (l *Log) rotateLocked() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	for i := l.keep; i >= 1; i-- {
		source := l.Path
		if i > 1 {
			source = fmt.Sprintf("%s.%d", l.Path, i-1)
		}
		target := fmt.Sprintf("%s.%d", l.Path, i)
		_ = os.Remove(target)
		_ = os.Rename(source, target)
	}
	l.openLocked()
	if l.size > l.maxBytes {
		// 改名失败（Windows 上别的进程正开着它）：直接截断，总比无限增长好。
		if l.file != nil {
			_ = l.file.Truncate(0)
			l.size = 0
		}
	}
}

// Tail 返回日志末尾 n 行。
func (l *Log) Tail(n int) string { return Tail(l.Path, n, 64<<10) }

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Tail 读任意日志文件的末尾 n 行（只读，不阻碍写入方轮转）。
func Tail(path string, n int, maxBytes int64) string {
	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	f, err := openShared(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxBytes))
	return LastLines(string(bytes.ToValidUTF8(data, []byte("?"))), n)
}

// LastLines 返回文本的最后 n 个非空行。
func LastLines(text string, n int) string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
