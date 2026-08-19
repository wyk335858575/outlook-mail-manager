#!/usr/bin/env sh
set -eu

DEPLOY_DIR="${1:-/www/wwwroot/outlook-mail-manager}"
ENV_FILE="$DEPLOY_DIR/.env"
ARCH="$(uname -m)"
case "$ARCH" in
x86_64) BINARY="outlook-mail-manager-updater-linux-amd64" ;;
aarch64 | arm64) BINARY="outlook-mail-manager-updater-linux-arm64" ;;
*)
	echo "Unsupported architecture: $ARCH" >&2
	exit 1
	;;
esac

test "$(id -u)" -eq 0 || {
	echo "Run this installer as root." >&2
	exit 1
}
test -f "$DEPLOY_DIR/$BINARY" || {
	echo "Missing $DEPLOY_DIR/$BINARY" >&2
	exit 1
}
test -f "$ENV_FILE" || {
	echo "Missing $ENV_FILE; configure the deployment before installing the updater." >&2
	exit 1
}
command -v docker >/dev/null
command -v cosign >/dev/null
command -v sha256sum >/dev/null

env_value() {
	sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1 | tr -d '\r'
}

REPOSITORY="$(env_value APP_UPDATE_REPOSITORY)"
IMAGE_VALUE="$(env_value APP_IMAGE)"
VERSION="$(env_value APP_VERSION)"
IMAGE="${IMAGE_VALUE%%@*}"
case "$IMAGE" in
*:*) IMAGE="${IMAGE%:*}" ;;
esac

printf '%s' "$REPOSITORY" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || {
	echo "APP_UPDATE_REPOSITORY is invalid." >&2
	exit 1
}
printf '%s' "$IMAGE" | grep -Eq '^ghcr\.io/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || {
	echo "APP_IMAGE must be a GHCR image." >&2
	exit 1
}
printf '%s' "$VERSION" | grep -Eq '^1\.0\.(0|[1-9][0-9]*)$' || {
	echo "APP_VERSION must use 1.0.N." >&2
	exit 1
}

IDENTITY="https://github.com/$REPOSITORY/.github/workflows/release.yml@refs/tags/v$VERSION"
for FILE in "$BINARY" SHA256SUMS; do
	test -f "$DEPLOY_DIR/$FILE" || {
		echo "Missing $DEPLOY_DIR/$FILE" >&2
		exit 1
	}
	test -f "$DEPLOY_DIR/$FILE.bundle" || {
		echo "Missing $DEPLOY_DIR/$FILE.bundle" >&2
		exit 1
	}
	cosign verify-blob \
		--bundle "$DEPLOY_DIR/$FILE.bundle" \
		--certificate-identity "$IDENTITY" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		"$DEPLOY_DIR/$FILE" >/dev/null
done
CHECKSUM_LINE="$(grep "  $BINARY\$" "$DEPLOY_DIR/SHA256SUMS" || true)"
test -n "$CHECKSUM_LINE" || {
	echo "$BINARY is missing from SHA256SUMS." >&2
	exit 1
}
(cd "$DEPLOY_DIR" && printf '%s\n' "$CHECKSUM_LINE" | sha256sum -c -)

getent group outlook-mail-manager >/dev/null 2>&1 || groupadd --system outlook-mail-manager
install -d -m 0750 /etc/outlook-mail-manager
install -m 0755 "$DEPLOY_DIR/$BINARY" /usr/local/libexec/outlook-mail-manager-updater
install -m 0644 "$DEPLOY_DIR/deploy/outlook-mail-manager-updater.service" /etc/systemd/system/outlook-mail-manager-updater.service
install -d -m 0755 /etc/systemd/system/outlook-mail-manager-updater.service.d
DROP_IN_TMP="$(mktemp)"
printf '[Service]\nReadWritePaths=%s\n' "$DEPLOY_DIR" >"$DROP_IN_TMP"
install -m 0644 "$DROP_IN_TMP" /etc/systemd/system/outlook-mail-manager-updater.service.d/deploy.conf
CONFIG_TMP="$(mktemp)"
trap 'rm -f "$CONFIG_TMP" "$DROP_IN_TMP"' EXIT
{
	printf 'APP_UPDATE_REPOSITORY=%s\n' "$REPOSITORY"
	printf 'APP_IMAGE=%s\n' "$IMAGE"
	printf 'UPDATER_DEPLOY_DIR=%s\n' "$DEPLOY_DIR"
	printf 'UPDATER_STATE_DIR=/var/lib/outlook-mail-manager-updater\n'
	printf 'UPDATER_SOCKET_PATH=/run/outlook-mail-manager-updater/updater.sock\n'
	printf 'UPDATER_COMPOSE_SERVICE=app\n'
	printf 'UPDATER_HEALTH_URL=http://127.0.0.1:8080/healthz\n'
	printf 'UPDATER_COSIGN_OIDC_ISSUER=https://token.actions.githubusercontent.com\n'
} >"$CONFIG_TMP"
install -m 0640 "$CONFIG_TMP" /etc/outlook-mail-manager/updater.env

GID="$(getent group outlook-mail-manager | cut -d: -f3)"
if grep -q '^UPDATER_SOCKET_GID=' "$ENV_FILE"; then
	sed -i "s/^UPDATER_SOCKET_GID=.*/UPDATER_SOCKET_GID=$GID/" "$ENV_FILE"
else
	printf '\nUPDATER_SOCKET_GID=%s\n' "$GID" >>"$ENV_FILE"
fi

systemctl daemon-reload
systemctl enable --now outlook-mail-manager-updater.service
docker compose --project-directory "$DEPLOY_DIR" --env-file "$DEPLOY_DIR/.env" up -d app
echo "Updater installed. Verify with: systemctl status outlook-mail-manager-updater"
