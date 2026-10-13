#!/bin/sh
# 下载内置的 mihomo 核心与地理数据库到 Vendor/core/，打包脚本从这里取用。
#
#   scripts/fetch-core.sh                 按 core.lock 下载并校验（已有且校验通过则跳过）
#   scripts/fetch-core.sh --update-core   查询最新 mihomo 发布，改写 core.lock
#   scripts/fetch-core.sh --refresh-geo   强制重新下载 GeoIP/GeoSite
#
# 地理数据库（meta-rules-dat 的滚动 "latest" 发布）每天更新，所以不写进 lock：
# 每次取当天最新，并用 GitHub API 给出的 sha256 摘要校验下载完整性。
# 设置 GITHUB_TOKEN 可以避免匿名 API 限流。
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
out="$root/Vendor/core"
lock="$root/core.lock"
mkdir -p "$out"

api() {
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		curl -fsSL --max-time 30 -H "Authorization: Bearer $GITHUB_TOKEN" "$1"
	else
		curl -fsSL --max-time 30 "$1"
	fi
}

sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }

if [ "${1:-}" = "--update-core" ]; then
	json="$(api https://api.github.com/repos/MetaCubeX/mihomo/releases/latest)"
	printf '%s' "$json" | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
v = d["tag_name"]
want = {"ARM64": "mihomo-darwin-arm64-%s.gz" % v, "AMD64": "mihomo-darwin-amd64-v1-%s.gz" % v}
assets = {a["name"]: a for a in d["assets"]}
print("# 内置核心的固定版本。sha256 是 GitHub 发布资产（.gz）的摘要，fetch-core.sh 下载后逐项校验。")
print("# 升级核心：scripts/fetch-core.sh --update-core，然后重新跑测试再提交。")
print("MIHOMO_VERSION=%s" % v)
for k, name in want.items():
    digest = assets[name]["digest"].split(":", 1)[1]
    print("MIHOMO_%s_ASSET=%s" % (k, name))
    print("MIHOMO_%s_SHA256=%s" % (k, digest))
' >"$lock.new"
	mv "$lock.new" "$lock"
	printf '已更新 %s\n' "$lock"
	cat "$lock"
	exit 0
fi

# shellcheck disable=SC1090
. "$lock"

fetch_core() {
	arch="$1" asset="$2" want="$3"
	dest="$out/mihomo-$arch"
	stamp="$dest.sha256"
	if [ -x "$dest" ] && [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$want" ]; then
		printf 'mihomo-%s %s 已就绪\n' "$arch" "$MIHOMO_VERSION"
		return
	fi
	printf '下载 %s …\n' "$asset"
	tmp="$(mktemp "${TMPDIR:-/tmp}/mihomo.XXXXXX")"
	curl -fsSL --max-time 300 -o "$tmp" "https://github.com/MetaCubeX/mihomo/releases/download/$MIHOMO_VERSION/$asset"
	got="$(sha256_of "$tmp")"
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		printf '校验失败：%s\n  期望 %s\n  实际 %s\n' "$asset" "$want" "$got" >&2
		exit 1
	fi
	gunzip -c "$tmp" >"$dest"
	rm -f "$tmp"
	chmod 755 "$dest"
	printf '%s' "$want" >"$stamp"
}

fetch_core arm64 "$MIHOMO_ARM64_ASSET" "$MIHOMO_ARM64_SHA256"
fetch_core amd64 "$MIHOMO_AMD64_ASSET" "$MIHOMO_AMD64_SHA256"

if [ "${1:-}" = "--refresh-geo" ] || [ ! -f "$out/geoip.metadb" ] || [ ! -f "$out/geosite.dat" ]; then
	json="$(api https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/latest)"
	for name in geoip.metadb geosite.dat; do
		want="$(printf '%s' "$json" | NAME="$name" python3 -I -c '
import json, os, sys
d = json.load(sys.stdin)
for a in d["assets"]:
    if a["name"] == os.environ["NAME"]:
        print(a["digest"].split(":", 1)[1])
')"
		printf '下载 %s …\n' "$name"
		tmp="$(mktemp "${TMPDIR:-/tmp}/geo.XXXXXX")"
		curl -fsSL --max-time 300 -o "$tmp" "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/$name"
		got="$(sha256_of "$tmp")"
		if [ -z "$want" ] || [ "$got" != "$want" ]; then
			rm -f "$tmp"
			printf '校验失败：%s（期望 %s，实际 %s）\n' "$name" "${want:-<无摘要>}" "$got" >&2
			exit 1
		fi
		mv "$tmp" "$out/$name"
		chmod 644 "$out/$name"
	done
	printf '%s' "$json" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["published_at"])' >"$out/geo-published-at"
fi

# 组件清单（components.json）由 build-app.sh 按最终打进 .app 的文件生成。
geo_date="$(cat "$out/geo-published-at" 2>/dev/null || echo unknown)"
printf '内置核心就绪：mihomo %s，地理库 %s\n' "$MIHOMO_VERSION" "$geo_date"
