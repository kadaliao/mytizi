#!/bin/sh
# 构建 MyTizi.app：通用二进制、内置 mihomo 与 GeoIP/GeoSite、ad-hoc 签名。
#
# 环境变量：
#   VERSION  版本号，默认 0.1.0（Info.plist 与组件清单都会写入）
#   OUT      输出目录，默认 dist/
#   ARCHS    目标架构，默认 "arm64 x86_64"（通用二进制）；本机调试可设为 arm64
#   DMG=1    另外输出 MyTizi-<版本>.dmg
#
# 没有 Developer ID，所以只做 ad-hoc 签名、不公证：分发出去的 App 首次打开
# 需要在「系统设置 → 隐私与安全性」点一次「仍要打开」（dmg 里附了说明）。
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
APP_NAME="MyTizi"
BUNDLE_ID="com.mytizi.app"
VERSION="${VERSION:-0.1.0}"
OUT="${OUT:-$root/dist}"
ARCHS="${ARCHS:-arm64 x86_64}"
IPC_VERSION="$(sed -n 's/.*ipcVersion = \([0-9][0-9]*\).*/\1/p' "$root/Sources/MytiziKit/AppInfo.swift")"

short_version="$(printf '%s\n' "$VERSION" | sed -n 's/^v\{0,1\}\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')"
short_version="${short_version:-0.0.0}"

work="$(mktemp -d "${TMPDIR:-/tmp}/mytizi-build.XXXXXX")"
trap 'rm -rf "$work"' EXIT HUP INT TERM

"$root/scripts/fetch-core.sh"
core_dir="$root/Vendor/core"

# SwiftPM 默认把 SDK 版本记成部署目标；显式写入实际 SDK 版本，最低系统要求仍是 macOS 13。
sdk_version="$(xcrun --show-sdk-version)"
link_flags="-Xlinker -platform_version -Xlinker macos -Xlinker 13.0 -Xlinker $sdk_version"

cd "$root"
# 逐个架构构建再 lipo 合并（一次传多个 --arch 会改走 XCBuild，Xcode 26 下 -Xlinker 会失败）。
# 每个架构使用独立 scratch 目录，避免产物互相覆盖。
for arch in $ARCHS; do
	# shellcheck disable=SC2086
	swift build -c release --arch "$arch" --scratch-path ".build/$arch" $link_flags
done
products="$work/products"
mkdir -p "$products"
for product in MyTizi mytizi-helper; do
	set --
	for arch in $ARCHS; do
		# shellcheck disable=SC2086
		set -- "$@" "$(swift build -c release --arch "$arch" --scratch-path ".build/$arch" $link_flags --show-bin-path)/$product"
	done
	lipo -create "$@" -output "$products/$product"
done

# 核心：按 ARCHS 取对应架构的 mihomo，多架构时合并成通用二进制。
set --
for arch in $ARCHS; do
	case "$arch" in
	arm64) set -- "$@" "$core_dir/mihomo-arm64" ;;
	x86_64) set -- "$@" "$core_dir/mihomo-amd64" ;;
	esac
done
lipo -create "$@" -output "$products/mihomo"

# 图标
swift "$root/scripts/make-icon.swift" "$work/icon.png"
iconset="$work/AppIcon.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
	sips -z "$size" "$size" "$work/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
	double=$((size * 2))
	sips -z "$double" "$double" "$work/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$work/AppIcon.icns"

app="$work/$APP_NAME.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/core" "$app/Contents/Helpers"
cp "$products/MyTizi" "$app/Contents/MacOS/$APP_NAME"
cp "$products/mytizi-helper" "$app/Contents/Helpers/mytizi-helper"
cp "$products/mihomo" "$app/Contents/Resources/core/mihomo"
cp "$core_dir/geoip.metadb" "$core_dir/geosite.dat" "$app/Contents/Resources/core/"
cp "$work/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
# 许可证：MyTizi 自身（MIT）、第三方声明、随包分发的 mihomo（GPL-3.0）许可证全文。
mkdir -p "$app/Contents/Resources/licenses"
cp "$root/LICENSE" "$app/Contents/Resources/licenses/MyTizi-LICENSE.txt"
cp "$root/THIRD_PARTY_NOTICES.md" "$app/Contents/Resources/licenses/THIRD_PARTY_NOTICES.md"
cp "$core_dir/mihomo-LICENSE.txt" "$app/Contents/Resources/licenses/mihomo-LICENSE.txt"
chmod 755 "$app/Contents/MacOS/$APP_NAME" "$app/Contents/Helpers/mytizi-helper" "$app/Contents/Resources/core/mihomo"

cat >"$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key><string>zh-Hans</string>
	<!-- 声明支持的语言：系统提供的控件（粘贴按钮、标准菜单等）按它决定显示中文还是英文。 -->
	<key>CFBundleLocalizations</key><array><string>zh-Hans</string><string>en</string></array>
	<key>CFBundleExecutable</key><string>$APP_NAME</string>
	<key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
	<key>CFBundleName</key><string>$APP_NAME</string>
	<key>CFBundleDisplayName</key><string>$APP_NAME</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundleShortVersionString</key><string>$short_version</string>
	<key>CFBundleVersion</key><string>$short_version</string>
	<key>LSMinimumSystemVersion</key><string>13.0</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.utilities</string>
	<key>NSHighResolutionCapable</key><true/>
	<!-- 订阅链接可能是明文 http（自建面板、内网地址）；不放开的话打包后的 App 会被 ATS 拦截。
	     https 链接仍然照常校验证书。 -->
	<key>NSAppTransportSecurity</key>
	<dict><key>NSAllowsArbitraryLoads</key><true/></dict>
</dict>
</plist>
PLIST

# 先签内层可执行文件（签名会改变文件内容），再据此生成组件清单，最后签外层 .app。
# 清单里的哈希必须是最终打进包里的文件的哈希，安装器和 App 都靠它判断组件是否需要更新。
codesign --force --sign - --timestamp=none "$app/Contents/Helpers/mytizi-helper"
codesign --force --sign - --timestamp=none "$app/Contents/Resources/core/mihomo"

sha() { shasum -a 256 "$1" | awk '{print $1}'; }
cat >"$app/Contents/Resources/core/components.json" <<JSON
{
  "core": "$(sed -n 's/^MIHOMO_VERSION=//p' "$root/core.lock")",
  "coreSha256": "$(sha "$app/Contents/Resources/core/mihomo")",
  "geoipSha256": "$(sha "$app/Contents/Resources/core/geoip.metadb")",
  "geositeSha256": "$(sha "$app/Contents/Resources/core/geosite.dat")",
  "helperSha256": "$(sha "$app/Contents/Helpers/mytizi-helper")",
  "geoPublishedAt": "$(cat "$core_dir/geo-published-at")",
  "ipcVersion": $IPC_VERSION,
  "appVersion": "$VERSION"
}
JSON

codesign --force --sign - --timestamp=none "$app"
codesign --verify --strict "$app"

mkdir -p "$OUT"
rm -rf "$OUT/$APP_NAME.app"
ditto "$app" "$OUT/$APP_NAME.app"

if [ "${DMG:-}" = "1" ]; then
	dmg_name="$APP_NAME-$VERSION.dmg"
	stage="$work/dmg"
	mkdir -p "$stage"
	ditto "$app" "$stage/$APP_NAME.app"
	ln -s /Applications "$stage/Applications"
	cp "$root/scripts/dmg-readme.txt" "$stage/首次打开请先看这里.txt"
	rm -f "$OUT/$dmg_name"
	hdiutil create -quiet -volname "$APP_NAME" -srcfolder "$stage" -fs HFS+ -format UDZO "$OUT/$dmg_name"
	printf '已生成 %s\n' "$OUT/$dmg_name"
fi
printf '已生成 %s（%s，%s）\n' "$OUT/$APP_NAME.app" "$VERSION" "$ARCHS"
