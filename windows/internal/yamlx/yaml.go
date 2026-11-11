// Package yamlx 是极小的 YAML 输出器，只覆盖生成 mihomo 配置所需的子集。
//
// 不引入第三方 YAML 库的原因：配置只需要"写"，不需要"读"；订阅内容交给 mihomo 自己解析。
// 所有字符串一律加双引号，避免 no、on、+.lan、198.18.0.1/16 之类被 YAML 当成别的类型。
package yamlx

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Value 是 YAML 值：String、Int、Bool、Array、Map 之一。
type Value interface{ isValue() }

type String string
type Int int
type Bool bool
type Array []Value

// Pair 是有序映射里的一项。
type Pair struct {
	Key   string
	Value Value
}

// Map 保持插入顺序（mihomo 不在乎顺序，但人读配置时在乎）。
type Map []Pair

func (String) isValue() {}
func (Int) isValue()    {}
func (Bool) isValue()   {}
func (Array) isValue()  {}
func (Map) isValue()    {}

// Strings 把字符串切片包装成数组。
func Strings(values []string) Array {
	out := make(Array, len(values))
	for i, v := range values {
		out[i] = String(v)
	}
	return out
}

// Render 输出完整文档，以换行结尾。
func Render(v Value) string {
	var lines []string
	switch t := v.(type) {
	case Map:
		emitMap(t, 0, &lines)
	case Array:
		emitArray(t, 0, &lines)
	default:
		lines = append(lines, scalar(v))
	}
	return strings.Join(lines, "\n") + "\n"
}

func emitMap(pairs Map, indent int, lines *[]string) {
	pad := strings.Repeat(" ", indent)
	for _, p := range pairs {
		k := key(p.Key)
		switch inner := p.Value.(type) {
		case Map:
			if len(inner) == 0 {
				*lines = append(*lines, pad+k+": {}")
			} else {
				*lines = append(*lines, pad+k+":")
				emitMap(inner, indent+2, lines)
			}
		case Array:
			if len(inner) == 0 {
				*lines = append(*lines, pad+k+": []")
			} else {
				*lines = append(*lines, pad+k+":")
				emitArray(inner, indent+2, lines)
			}
		default:
			*lines = append(*lines, pad+k+": "+scalar(p.Value))
		}
	}
}

func emitArray(items Array, indent int, lines *[]string) {
	pad := strings.Repeat(" ", indent)
	for _, item := range items {
		switch inner := item.(type) {
		case Map:
			if len(inner) == 0 {
				*lines = append(*lines, pad+"- {}")
				continue
			}
			// 第一对跟在 "- " 后面，其余按 indent+2 对齐。
			var sub []string
			emitMap(inner, indent+2, &sub)
			sub[0] = pad + "- " + sub[0][indent+2:]
			*lines = append(*lines, sub...)
		case Array:
			*lines = append(*lines, pad+"-")
			emitArray(inner, indent+2, lines)
		default:
			*lines = append(*lines, pad+"- "+scalar(item))
		}
	}
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func key(k string) string {
	if plainKey.MatchString(k) {
		return k
	}
	return Quote(k)
}

func scalar(v Value) string {
	switch t := v.(type) {
	case String:
		return Quote(string(t))
	case Int:
		return strconv.Itoa(int(t))
	case Bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return "null"
	}
}

// Quote 输出双引号字符串。JSON 转义是 YAML 双引号转义的子集，可以直接复用规则。
func Quote(text string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7F {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
