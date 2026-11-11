#!/bin/sh
# 重新生成 Windows 版的图标（需要 macOS：用到 swift 与 sips；产物已提交，平时不用跑）。
#
#   windows/scripts/make-icons.sh
#
# 产物：
#   windows/build/icon.ico               App 图标：16–48 px 用 tools/makeicons/glyph.swift 重画（方块铺满、线条加粗），
#                                        64–256 px 取自 macOS 版图标（scripts/make-icon.swift），裁掉 macOS 的大边距
#   windows/build/appicon.png            256 px 预览
#   windows/cmd/mytizi/icons/tray-on.ico   托盘·已连接（绿色）
#   windows/cmd/mytizi/icons/tray-off.ico  托盘·未连接（灰色）
set -eu

windows="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
root="$(dirname "$windows")"
work="$(mktemp -d "${TMPDIR:-/tmp}/mytizi-icons.XXXXXX")"
trap 'rm -rf "$work"' EXIT HUP INT TERM

glyph() { swift "$windows/tools/makeicons/glyph.swift" "$@"; }

# App 图标
swift "$root/scripts/make-icon.swift" "$work/mac-1024.png"
# macOS 图标在 1024 画布上留了 100 px 边距；Windows 图标铺得更满，只留 24 px。
sips -c 872 872 "$work/mac-1024.png" --out "$work/app-full.png" >/dev/null
set --
for size in 16 20 24 32 40 48; do
	glyph "$work/app-$size.png" "$size" brand
	set -- "$@" "$work/app-$size.png"
done
for size in 64 96 128 256; do
	sips -z "$size" "$size" "$work/app-full.png" --out "$work/app-$size.png" >/dev/null
	set -- "$@" "$work/app-$size.png"
done
mkdir -p "$windows/build"
(cd "$windows" && go run tools/makeicons/ico.go "$windows/build/icon.ico" "$@")
cp "$work/app-256.png" "$windows/build/appicon.png"

# 托盘图标
mkdir -p "$windows/cmd/mytizi/icons"
for state in on off; do
	set --
	for size in 16 20 24 32 40 48; do
		glyph "$work/tray-$state-$size.png" "$size" "$state"
		set -- "$@" "$work/tray-$state-$size.png"
	done
	(cd "$windows" && go run tools/makeicons/ico.go "$windows/cmd/mytizi/icons/tray-$state.ico" "$@")
done

# 预览（需要时设 PREVIEW_DIR，把各尺寸 PNG 留下来看）
if [ -n "${PREVIEW_DIR:-}" ]; then
	mkdir -p "$PREVIEW_DIR"
	cp "$work"/*.png "$PREVIEW_DIR"/
fi
