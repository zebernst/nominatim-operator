#!/bin/sh
# Write theme config from env, then exec nginx (or whatever CMD).
# NOMINATIM_API_ENDPOINT is the browser-reachable Nominatim API base URL
# (trailing slash added if missing). Defaults to "/" for same-origin proxies.
set -eu

ENDPOINT="${NOMINATIM_API_ENDPOINT:-/}"
case "${ENDPOINT}" in
*/) ;;
*) ENDPOINT="${ENDPOINT}/" ;;
esac

# Escape backslashes and single quotes for a single-quoted JS string literal.
js_escape() {
	printf '%s' "$1" | sed "s/\\\\/\\\\\\\\/g; s/'/\\\\'/g"
}

THEME_DIR="/usr/share/nginx/html/theme"
mkdir -p "${THEME_DIR}"
cat >"${THEME_DIR}/config.theme.js" <<EOF
Nominatim_Config.Nominatim_API_Endpoint = '$(js_escape "${ENDPOINT}")';
EOF

exec "$@"
