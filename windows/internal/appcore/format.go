package appcore

import (
	"fmt"
	"strconv"
	"time"
)

func itoa(n int) string { return strconv.Itoa(n) }

// Bytes 按 1024 进位、保留两位小数，单位写法与 macOS 版一致（"40.05 GB"）。
func Bytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d 字节", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	unit := ""
	for _, u := range units {
		value /= 1024
		unit = u
		if value < 1024 {
			break
		}
	}
	if value >= 100 {
		return fmt.Sprintf("%.1f %s", value, unit)
	}
	return fmt.Sprintf("%.2f %s", value, unit)
}

// DateText 输出"2027年1月18日"。
func DateText(t time.Time) string {
	t = t.Local()
	return fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day())
}

// UpdatedText 输出"3小时前更新"之类的相对时间。
func UpdatedText(updated *time.Time, now time.Time) string {
	if updated == nil {
		return "尚未更新"
	}
	d := now.Sub(*updated)
	switch {
	case d < time.Minute:
		return "刚刚更新"
	case d < time.Hour:
		return fmt.Sprintf("%d分钟前更新", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时前更新", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d天前更新", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d周前更新", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("%d个月前更新", int(d.Hours()/24/30))
	}
}
