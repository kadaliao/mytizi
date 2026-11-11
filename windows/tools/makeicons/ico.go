//go:build ignore

// 把若干 PNG 打包成一个 .ico（每个条目直接存 PNG，Windows Vista 起都支持）。
// 用法：go run tools/makeicons/ico.go <输出.ico> <a.png> [b.png ...]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"os"
	"sort"
)

type entry struct {
	size int
	data []byte
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "用法：go run ico.go <输出.ico> <a.png> [b.png ...]")
		os.Exit(64)
	}
	var entries []entry
	for _, path := range os.Args[2:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		img, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			fail(fmt.Errorf("%s：%w", path, err))
		}
		if img.Width != img.Height || img.Width > 256 {
			fail(fmt.Errorf("%s 是 %dx%d，ICO 条目必须是不大于 256 的正方形", path, img.Width, img.Height))
		}
		entries = append(entries, entry{img.Width, data})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size < entries[j].size })

	var out bytes.Buffer
	write := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	write(uint16(0))            // 保留
	write(uint16(1))            // 类型：图标
	write(uint16(len(entries))) // 条目数
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		dim := uint8(e.size)
		if e.size == 256 {
			dim = 0 // 256 记作 0
		}
		write(dim)        // 宽
		write(dim)        // 高
		write(uint8(0))   // 调色板颜色数
		write(uint8(0))   // 保留
		write(uint16(1))  // 颜色平面
		write(uint16(32)) // 位深
		write(uint32(len(e.data)))
		write(uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}
	if err := os.WriteFile(os.Args[1], out.Bytes(), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("%s：%d 个尺寸，%d 字节\n", os.Args[1], len(entries), out.Len())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "失败：", err)
	os.Exit(1)
}
