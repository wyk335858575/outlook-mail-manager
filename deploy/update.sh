#!/usr/bin/env sh
set -eu

DEPLOY_DIR="${DEPLOY_DIR:-/www/wwwroot/outlook-mail-manager}"
ENV_FILE="$DEPLOY_DIR/.env"
DEFAULT_REPOSITORY="wyk335858575/outlook-mail-manager"
DEFAULT_IMAGE="ghcr.io/wyk335858575/outlook-mail-manager"
COSIGN_VERSION="v2.6.4"
COSIGN_OIDC_ISSUER="https://token.actions.githubusercontent.com"

umask 077
TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT HUP INT TERM

log() {
	printf '\n==> %s\n' "$1"
}

fail() {
	printf '错误：%s\n' "$1" >&2
	exit 1
}

test "$(id -u)" -eq 0 || fail "请在宝塔 root 终端中执行此命令"
for command in curl docker sha256sum awk grep sed; do
	command -v "$command" >/dev/null 2>&1 || fail "服务器缺少命令：$command"
done
test -f "$ENV_FILE" || fail "找不到 $ENV_FILE，请确认项目部署目录"
docker compose version >/dev/null 2>&1 || fail "需要 Docker Compose v2"

env_value() {
	sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1 | tr -d '\r'
}

REPOSITORY="$(env_value APP_UPDATE_REPOSITORY)"
test -n "$REPOSITORY" || REPOSITORY="$DEFAULT_REPOSITORY"
printf '%s' "$REPOSITORY" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || fail "APP_UPDATE_REPOSITORY 格式无效"

IMAGE_VALUE="$(env_value APP_IMAGE)"
test -n "$IMAGE_VALUE" || IMAGE_VALUE="$DEFAULT_IMAGE"
IMAGE="${IMAGE_VALUE%%@*}"
case "$IMAGE" in
*:*) IMAGE="${IMAGE%:*}" ;;
esac
printf '%s' "$IMAGE" | grep -Eq '^ghcr\.io/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || fail "APP_IMAGE 必须是 GHCR 镜像"

CURRENT_VERSION="$(env_value APP_VERSION)"
test -n "$CURRENT_VERSION" || fail ".env 中缺少 APP_VERSION"

case "$(uname -m)" in
x86_64)
	UPDATER_ASSET="outlook-mail-manager-updater-linux-amd64"
	COSIGN_ASSET="cosign-linux-amd64"
	COSIGN_SHA256="309779b0c4e409186b0a80daba99041fe2cf65a920ce645013901df6211895a9"
	;;
aarch64 | arm64)
	UPDATER_ASSET="outlook-mail-manager-updater-linux-arm64"
	COSIGN_ASSET="cosign-linux-arm64"
	COSIGN_SHA256="df408e5418129306fed7349ec46e27be0445d05c5127c07f435e9a566af67593"
	;;
*) fail "不支持的服务器架构：$(uname -m)" ;;
esac

log "检查 GitHub 最新稳定版"
LATEST_URL="$(curl -fsSL --retry 3 -o /dev/null -w '%{url_effective}' "https://github.com/$REPOSITORY/releases/latest")"
TAG="${LATEST_URL##*/}"
TAG="${TAG%%\?*}"
printf '%s' "$TAG" | grep -Eq '^v1\.0\.(0|[1-9][0-9]*)$' || fail "最新 Release 不是受支持的 v1.0.N 正式版本"
VERSION="${TAG#v}"
if test "$CURRENT_VERSION" = "$VERSION"; then
	printf '当前已是最新稳定版 %s，无需更新。\n' "$VERSION"
	exit 0
fi

RELEASE_BASE="https://github.com/$REPOSITORY/releases/download/$TAG"
for asset in "$UPDATER_ASSET" "$UPDATER_ASSET.bundle" SHA256SUMS SHA256SUMS.bundle release-manifest.json release-manifest.json.bundle; do
	curl -fsSL --retry 3 "$RELEASE_BASE/$asset" -o "$TEMP_DIR/$asset"
done

log "下载并校验临时 Cosign 验证器"
COSIGN_BASE="https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION"
curl -fsSL --retry 3 "$COSIGN_BASE/$COSIGN_ASSET" -o "$TEMP_DIR/$COSIGN_ASSET"
printf '%s  %s\n' "$COSIGN_SHA256" "$COSIGN_ASSET" | (cd "$TEMP_DIR" && sha256sum -c -)
chmod 0700 "$TEMP_DIR/$COSIGN_ASSET"
COSIGN="$TEMP_DIR/$COSIGN_ASSET"
IDENTITY="https://github.com/$REPOSITORY/.github/workflows/release.yml@refs/tags/$TAG"

log "验证 Release 身份和文件完整性"
for asset in SHA256SUMS release-manifest.json "$UPDATER_ASSET"; do
	"$COSIGN" verify-blob \
		--bundle "$TEMP_DIR/$asset.bundle" \
		--certificate-identity "$IDENTITY" \
		--certificate-oidc-issuer "$COSIGN_OIDC_ISSUER" \
		"$TEMP_DIR/$asset" >/dev/null
done
CHECKSUM_LINE="$(grep "  $UPDATER_ASSET\$" "$TEMP_DIR/SHA256SUMS" || true)"
test -n "$CHECKSUM_LINE" || fail "$UPDATER_ASSET 不在已签名的 SHA256SUMS 中"
printf '%s\n' "$CHECKSUM_LINE" | (cd "$TEMP_DIR" && sha256sum -c -)
chmod 0700 "$TEMP_DIR/$UPDATER_ASSET"

set_env_value() {
	key="$1"
	value="$2"
	temporary="$(mktemp "$DEPLOY_DIR/.env.update.XXXXXX")"
	awk -v key="$key" -v value="$value" '
		BEGIN { found = 0 }
		index($0, key "=") == 1 { print key "=" value; found = 1; next }
		{ sub(/\r$/, ""); print }
		END { if (!found) print key "=" value }
	' "$ENV_FILE" >"$temporary"
	chmod 0600 "$temporary"
	mv "$temporary" "$ENV_FILE"
}

set_env_value APP_UPDATE_REPOSITORY "$REPOSITORY"

log "创建备份并升级到 $VERSION"
APP_UPDATE_REPOSITORY="$REPOSITORY" \
	APP_IMAGE="$IMAGE" \
	UPDATER_DEPLOY_DIR="$DEPLOY_DIR" \
	UPDATER_STATE_DIR="$TEMP_DIR/state" \
	UPDATER_COMPOSE_SERVICE="app" \
	UPDATER_HEALTH_URL="http://127.0.0.1:8080/healthz" \
	UPDATER_COSIGN_BINARY="$COSIGN" \
	UPDATER_COSIGN_OIDC_ISSUER="$COSIGN_OIDC_ISSUER" \
	"$TEMP_DIR/$UPDATER_ASSET" --once

printf '\n升级成功：%s -> %s\n升级前数据库备份已保留。\n' "$CURRENT_VERSION" "$VERSION"
