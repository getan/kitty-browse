#!/bin/sh
# kitty-browse 安装脚本：检测当前操作系统 / 架构，从 GitHub Releases 下载最新的
# 预编译二进制，校验 sha256 后安装到常用的 PATH 目录。
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/getan/kitty-browse/master/install.sh | sh
#
# 可用环境变量覆盖默认行为：
#   KITTY_BROWSE_VERSION       指定版本（形如 v0.2.0），默认取最新 release
#   KITTY_BROWSE_INSTALL_DIR   安装目录，默认 /usr/local/bin（无写权限时回退到 ~/.local/bin）
#   KITTY_BROWSE_SKIP_CHECKSUM 设为 1 跳过 sha256 校验（不推荐）
#   KITTY_BROWSE_MIRROR        覆盖镜像回退列表（空格分隔的 URL 前缀）；
#                              设为 off/none 则只用直连 GitHub
#   GITHUB_TOKEN               可选，用于提高 GitHub API 速率限制
#
# Chromium 不会被安装：kitty-browse 只负责启动它。脚本最后会检查并给出提示，
# 缺 Chromium 不算安装失败。
set -eu

REPO="getan/kitty-browse"
BINARY="kitty-browse"

info() { printf '%s\n' "kitty-browse-install: $*" >&2; }
err()  { printf '%s\n' "kitty-browse-install: error: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || err "缺少依赖命令: $1"; }

# 1. 检测下载器。与 golder 的 install.sh 同一套逻辑：连接超时 5s，平均速度低于
#    50KB/s 持续 15s 即判死，避免大陆直连 GitHub 时耗死在龟速连接上。
if command -v curl >/dev/null 2>&1; then
	DL="curl -fsSL"
	DLO="curl -fsSL --connect-timeout 5 --speed-limit 51200 --speed-time 15 -o"
elif command -v wget >/dev/null 2>&1; then
	DL="wget -qO-"
	DLO="wget -q --timeout=20 --tries=1 -O"
else
	err "需要 curl 或 wget"
fi

case "$DL" in
curl*) DL_KIND="curl" ;;
*) DL_KIND="wget" ;;
esac

# api_get <url>：只用于 api.github.com。带 token 时请求头必须作为一个完整参数
# 传下去——曾经写成 api_auth='-H Authorization:\ Bearer\ $TOKEN' 再不加引号展开，
# 结果拆成 4 个参数：curl 把 token 当成 URL，既认证失败，又把凭据送进了 DNS 查询。
# 镜像永远走 $DL，不带任何凭据。
api_get() {
	url=$1
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		if [ "$DL_KIND" = "curl" ]; then
			curl -fsSL --connect-timeout 5 -H "Authorization: Bearer $GITHUB_TOKEN" "$url"
		else
			wget -q --timeout=20 --header="Authorization: Bearer $GITHUB_TOKEN" -O - "$url"
		fi
		return
	fi
	$DL "$url"
}

mirrors() {
	case "${KITTY_BROWSE_MIRROR:-}" in
	"") printf '%s\n' "https://ghfast.top/ https://ghproxy.net/ https://gh-proxy.com/ https://gh.zwy.one/" ;;
	off | none) : ;;
	*) printf '%s\n' "$KITTY_BROWSE_MIRROR" ;;
	esac
}

# download <dest> <url> [label]：归档走直连优先、失败后按镜像依次回退。
download() {
	dest=$1
	original=$2
	label=${3:-$2}

	set -- "$original"
	for m in $(mirrors); do
		set -- "$@" "$m$original"
	done

	while [ $# -gt 0 ]; do
		src=$1
		shift
		if $DLO "$dest" "$src"; then
			[ "$src" = "$original" ] || info "${label}: 镜像下载成功: $src"
			return 0
		fi
		if [ $# -gt 0 ]; then
			info "${label}: 下载失败（${src}），改试下一个源: $1"
		else
			info "${label}: 下载失败（${src}），已无更多镜像源"
		fi
	done
	return 1
}

# digest_of <file> <archive>：从 checksums.txt 中提取指定归档的 sha256。列位置
# 无关，兼容 sha256sum 的 "*文件名" 二进制标记。
digest_of() {
	awk -v f="$2" '{ n=$2; sub(/^\*/, "", n) } n == f { print $1; exit }' "$1"
}

# fetch_checksums <dest> <url>：校验文件的信任阶梯。直连 GitHub 是信任根；直连
# 失败时用镜像副本，但要求所有可用镜像对 $ARCHIVE 给出一致的 sha256，否则中止。
fetch_checksums() {
	dest=$1
	url=$2
	if $DLO "$dest" "$url"; then
		return 0
	fi
	info "checksums.txt: GitHub 直连失败，改用镜像并交叉校验（需各来源一致）"

	agree_digest=""
	agree_count=0
	agree_file=""
	agree_src=""
	conflict_src=""
	idx=0
	for m in $(mirrors); do
		idx=$((idx + 1))
		cand="$dest.cand$idx"
		if ! $DLO "$cand" "$m$url"; then
			rm -f "$cand"
			continue
		fi
		d=$(digest_of "$cand" "$ARCHIVE")
		if [ -z "$d" ]; then
			info "checksums.txt: 忽略 $m 的副本（不含 $ARCHIVE 的记录）"
			rm -f "$cand"
			continue
		fi
		if [ -z "$agree_digest" ]; then
			agree_digest=$d
			agree_count=1
			agree_file=$cand
			agree_src=$m
		elif [ "$d" = "$agree_digest" ]; then
			agree_count=$((agree_count + 1))
			agree_file=$cand
		else
			conflict_src=$m
			rm -f "$cand"
		fi
	done

	if [ -n "$conflict_src" ]; then
		rm -f "$dest".cand*
		err "checksums.txt 镜像交叉校验不一致：$agree_src 与 $conflict_src 对 $ARCHIVE 给出的 sha256 不同——可能被投毒，已中止"
	fi
	if [ -z "$agree_file" ]; then
		err "无法获取 checksums.txt：GitHub 直连与镜像均失败（可设 KITTY_BROWSE_MIRROR 指定镜像）"
	fi
	if [ "$agree_count" -ge 2 ]; then
		info "checksums.txt: $agree_count 个来源一致，采用镜像副本"
	else
		info "警告: checksums.txt 仅由 $agree_src 提供（GitHub 直连不可用，无法交叉验证）——继续安装"
	fi
	cp "$agree_file" "$dest"
	rm -f "$dest".cand*
	return 0
}

need tar
need uname

SHA256=""
if command -v sha256sum >/dev/null 2>&1; then
	SHA256="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	SHA256="shasum -a 256"
elif command -v openssl >/dev/null 2>&1; then
	SHA256="openssl dgst -sha256"
fi

# 2. 操作系统 → goreleaser 归档命名（见 .goreleaser.yaml）。
os_raw=$(uname -s)
case "$os_raw" in
	Linux)  OS="Linux" ;;
	Darwin) OS="Darwin" ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		err "Windows 暂不支持：控制 socket 与终端图形都需要 Unix 语义" ;;
	*) err "不支持的操作系统: $os_raw" ;;
esac

# 3. 架构 → 归档命名。
arch_raw=$(uname -m)
case "$arch_raw" in
	x86_64 | amd64) ARCH="x86_64" ;;
	arm64 | aarch64) ARCH="arm64" ;;
	*) err "不支持的架构: ${arch_raw}（目前发布 linux/darwin 的 amd64 与 arm64）" ;;
esac

# 4. 解析版本。版本查询和归档下载走同一份镜像列表：大陆直连 api.github.com
#    往往比 github.com 更先失败，只给下载配镜像等于没配。
API_URL="${KITTY_BROWSE_API:-https://api.github.com/repos/$REPO/releases/latest}"

tag_of() {
	printf '%s' "$1" | tr -d ' \n' | grep -oE '"tag_name":"[^"]*"' | head -n1 | cut -d'"' -f4
}

# resolve_version 依次尝试直连与镜像，返回第一个能解析出的 tag。
# GitHub token 只发给 api.github.com：镜像会看到请求头，不能让它拿到凭据。
resolve_version() {
	if json=$(api_get "$API_URL" 2>/dev/null); then
		if tag=$(tag_of "$json") && [ -n "$tag" ]; then
			printf '%s' "$tag"
			return 0
		fi
	fi
	# 显式指定了 API 地址（例如自建站点接口）就不再套镜像前缀。
	[ -n "${KITTY_BROWSE_API:-}" ] && return 1
	if [ -n "$(mirrors)" ]; then
		info "版本查询: GitHub 直连失败，改试镜像 ..."
	fi
	for m in $(mirrors); do
		if json=$($DL "$m$API_URL" 2>/dev/null); then
			if tag=$(tag_of "$json") && [ -n "$tag" ]; then
				# 版本号经镜像取得，但归档仍要靠交叉校验过的 checksums.txt
				# 才能安装，所以镜像最多能让版本变旧，不能让内容被替换。
				info "版本查询: 使用镜像 $m（归档仍会做 sha256 校验）"
				printf '%s' "$tag"
				return 0
			fi
		fi
	done
	return 1
}

VERSION="${KITTY_BROWSE_VERSION:-}"
if [ -z "$VERSION" ]; then
	info "查询最新 release ..."
	VERSION=$(resolve_version) || \
		err "无法查询最新版本（直连与镜像均失败），请用 KITTY_BROWSE_VERSION 指定版本"
	[ -n "$VERSION" ] || err "无法解析最新版本号，请用 KITTY_BROWSE_VERSION 指定"
fi

# 版本号字符集白名单，防止拼接出异常 URL / 文件名。
case "$VERSION" in
	*[!A-Za-z0-9._-]* | *..*) err "版本号含非法字符: $VERSION" ;;
esac

VER_NUM=$(printf '%s' "$VERSION" | sed 's/^v//')
ARCHIVE="${BINARY}_${VER_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE"

info "版本: $VERSION"
info "平台: ${OS}/${ARCH}"
info "下载: $URL"

TMP=$(mktemp -d 2>/dev/null || mktemp -d -t kitty-browse-install)
trap 'rm -rf "$TMP"' EXIT INT TERM
# ${URL} 的花括号是必须的：全角括号紧跟变量名时，bash 3.2 会把多字节字符吸进
# 变量名，在 set -u 下报 unbound variable 并掩盖真正的下载失败。
download "$TMP/$ARCHIVE" "$URL" "$ARCHIVE" || err "下载失败: ${URL}（可设 KITTY_BROWSE_MIRROR 指定镜像前缀）"

# 5. 校验后才解压。
if [ "${KITTY_BROWSE_SKIP_CHECKSUM:-}" = "1" ]; then
	info "已跳过 sha256 校验（KITTY_BROWSE_SKIP_CHECKSUM=1）"
else
	[ -n "$SHA256" ] || err "缺少 sha256 工具（sha256sum / shasum / openssl）；确需跳过请设 KITTY_BROWSE_SKIP_CHECKSUM=1"
	CHECKSUMS_URL="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"
	fetch_checksums "$TMP/checksums.txt" "$CHECKSUMS_URL" || \
		err "无法获取 checksums.txt（可设 KITTY_BROWSE_SKIP_CHECKSUM=1 跳过校验）"
	expected=$(digest_of "$TMP/checksums.txt" "$ARCHIVE")
	[ -n "$expected" ] || err "checksums.txt 中缺少 $ARCHIVE 的记录"
	actual=$($SHA256 "$TMP/$ARCHIVE" | awk '{ for (i = 1; i <= NF; i++) if (length($i) == 64) { print $i; exit } }')
	if [ "$actual" != "$expected" ]; then
		err "sha256 校验失败：期望 ${expected}，实际 ${actual} —— 下载可能被篡改，已中止安装"
	fi
	info "sha256 校验通过"
fi

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" || err "解压失败: $ARCHIVE"
[ -f "$TMP/$BINARY" ] || err "归档中未找到二进制 $BINARY"
chmod +x "$TMP/$BINARY"

# 6. 安装目录：KITTY_BROWSE_INSTALL_DIR > /usr/local/bin > ~/.local/bin。
DIR="${KITTY_BROWSE_INSTALL_DIR:-}"
if [ -z "$DIR" ]; then
	if [ -w /usr/local/bin ] 2>/dev/null; then
		DIR="/usr/local/bin"
	elif [ "$(id -u)" = "0" ]; then
		DIR="/usr/local/bin"
	else
		DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$DIR" 2>/dev/null || err "无法创建安装目录: $DIR"

DEST="$DIR/$BINARY"
if [ -w "$DIR" ]; then
	mv "$TMP/$BINARY" "$DEST"
elif command -v sudo >/dev/null 2>&1; then
	info "$DIR 需要提升权限，使用 sudo 安装 ..."
	sudo mv "$TMP/$BINARY" "$DEST"
else
	err "$DIR 不可写且无 sudo，请设置 KITTY_BROWSE_INSTALL_DIR 指向可写目录"
fi

info "已安装: $DEST"

case ":$PATH:" in
	*":$DIR:"*) : ;;
	*) info "注意: $DIR 不在 PATH 中，请将其加入 PATH，例如：" >&2
	   info "  echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" >&2 ;;
esac

"$DEST" -version || true

# 7. Chromium 不在本脚本职责内，但缺了它浏览器起不来，所以检查一次。
have_chromium=""
if [ -n "${KITTY_BROWSE_CHROME:-}" ] && [ -x "${KITTY_BROWSE_CHROME}" ]; then
	have_chromium="${KITTY_BROWSE_CHROME}"
else
	for cand in google-chrome chromium chromium-browser chrome headless_shell; do
		if p=$(command -v "$cand" 2>/dev/null); then
			have_chromium=$p
			break
		fi
	done
fi
if [ -z "$have_chromium" ] && [ "$OS" = "Darwin" ]; then
	for app in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
	           "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
		if [ -x "$app" ]; then
			have_chromium=$app
			break
		fi
	done
fi
if [ -n "$have_chromium" ]; then
	info "Chromium: $have_chromium"
else
	info "未找到 Chromium。kitty-browse 需要它才能渲染页面："
	info "  macOS: 安装 Google Chrome，或 brew install --cask chromium"
	info "  Linux: 安装 chromium 包，或设 KITTY_BROWSE_CHROME=/path/to/chrome"
fi
