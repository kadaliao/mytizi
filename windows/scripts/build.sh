#!/bin/sh
# 构建 Windows 安装包。在 macOS / Linux 上交叉编译即可，不需要 Windows。
#
# 环境变量：
#   VERSION   版本号，默认 dev（发布时由 CI 从 tag 填入，例如 0.4.0）
#   ARCHES    目标架构，默认 "amd64 arm64"
#   OUT       输出目录，默认 windows/dist
#   NSIS=0    只编译与拼装目录，不生成安装包（没装 makensis 时用）
#
# 产物：$OUT/MyTizi-<版本>-windows-{x64,arm64}-setup.exe 与 SHA256SUMS-windows.txt
# 依赖：Go；生成安装包还需要 makensis（macOS: brew install makensis；Ubuntu: apt install nsis）。
set -eu

here="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
root="$(CDPATH= cd -- "$here/.." && pwd)"
VERSION="${VERSION:-dev}"
VERSION="${VERSION#v}"
ARCHES="${ARCHES:-amd64 arm64}"
OUT="${OUT:-$here/dist}"
NSIS="${NSIS:-1}"
module="github.com/kadaliao/mytizi/windows"

numeric="$(printf '%s\n' "$VERSION" | sed -n 's/^\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')"
numeric="${numeric:-0.0.0}"

cd "$here"
go run ./tools/fetchcore
core_dir="$root/Vendor/core"

mkdir -p "$OUT"
work="$(mktemp -d "${TMPDIR:-/tmp}/mytizi-win.XXXXXX")"
trap 'rm -rf "$work"; rm -f "$here"/cmd/mytizi/rsrc_windows_*.syso "$here"/cmd/mytizi-service/rsrc_windows_*.syso' EXIT HUP INT TERM

# 界面要的图标从 build/ 复制进嵌入目录（go:embed 只能读包目录以内的文件）。
cp "$here/build/appicon.png" "$here/cmd/mytizi/icons/app.png"

# WebView2 引导安装器（约 2 MB）：缺运行时的 Windows 10 上由安装程序静默补装。下载失败不影响构建。
webview2="$here/build/cache/MicrosoftEdgeWebview2Setup.exe"
if [ "$NSIS" = 1 ] && [ ! -s "$webview2" ]; then
	mkdir -p "$(dirname "$webview2")"
	curl -fsSL --max-time 60 -o "$webview2" "https://go.microsoft.com/fwlink/p/?LinkId=2124703" || rm -f "$webview2"
fi

winres() {
	# $1 包目录  $2 manifest 类型  $3 文件说明  $4 原始文件名  $5 架构
	go run github.com/tc-hib/go-winres@v0.3.3 simply --arch "$5" --out "$1/rsrc" \
		--manifest "$2" --icon "$here/build/icon.ico" \
		--product-name MyTizi --file-description "$3" --original-filename "$4" \
		--product-version "$numeric.0" --file-version "$numeric.0" --copyright "MIT License"
}

for arch in $ARCHES; do
	case "$arch" in
	amd64) label=x64 ;;
	arm64) label=arm64 ;;
	*) echo "不支持的架构 $arch" >&2; exit 64 ;;
	esac
	stage="$work/stage-$label"
	mkdir -p "$stage/core" "$stage/licenses"

	winres cmd/mytizi gui "MyTizi" MyTizi.exe "$arch"
	winres cmd/mytizi-service cli "MyTizi 后台服务" mytizi-service.exe "$arch"
	ldver="-X $module/internal/appinfo.Version=$VERSION"
	GOOS=windows GOARCH="$arch" CGO_ENABLED=0 go build -tags production -trimpath -buildvcs=false \
		-ldflags "-s -w -H windowsgui $ldver" -o "$stage/MyTizi.exe" ./cmd/mytizi
	GOOS=windows GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -buildvcs=false \
		-ldflags "-s -w $ldver" -o "$stage/mytizi-service.exe" ./cmd/mytizi-service
	rm -f cmd/mytizi/rsrc_windows_*.syso cmd/mytizi-service/rsrc_windows_*.syso

	cp "$core_dir/mihomo-windows-$arch.exe" "$stage/core/mihomo.exe"
	# 保留地理库的修改时间：诊断按它判断数据库新旧。
	cp -p "$core_dir/geoip.metadb" "$core_dir/geosite.dat" "$stage/core/"
	cp "$root/LICENSE" "$stage/licenses/MyTizi-LICENSE.txt"
	cp "$core_dir/mihomo-LICENSE.txt" "$stage/licenses/mihomo-LICENSE.txt"
	cp "$root/THIRD_PARTY_NOTICES.md" "$stage/licenses/THIRD_PARTY_NOTICES.md"

	if [ "$NSIS" = 1 ]; then
		set -- -V2 -DVERSION="$VERSION" -DVERSION4="$numeric.0" -DARCH="$label" -DSTAGE="$stage" \
			-DOUTFILE="$OUT/MyTizi-$VERSION-windows-$label-setup.exe"
		[ -s "$webview2" ] && set -- "$@" -DWEBVIEW2_BOOTSTRAPPER="$webview2"
		makensis "$@" "$here/build/installer.nsi"
		echo "安装包：$OUT/MyTizi-$VERSION-windows-$label-setup.exe"
	else
		rm -rf "$OUT/MyTizi-$VERSION-windows-$label"
		cp -R "$stage" "$OUT/MyTizi-$VERSION-windows-$label"
		echo "目录：$OUT/MyTizi-$VERSION-windows-$label"
	fi
done

if [ "$NSIS" = 1 ]; then
	(cd "$OUT" && { command -v sha256sum >/dev/null && sha256sum MyTizi-"$VERSION"-windows-*-setup.exe || shasum -a 256 MyTizi-"$VERSION"-windows-*-setup.exe; } >SHA256SUMS-windows.txt)
	cat "$OUT/SHA256SUMS-windows.txt"
fi
