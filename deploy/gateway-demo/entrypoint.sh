#!/bin/sh
set -eu

data_dir=${LY_ROUTE_DATA_DIR:-/var/lib/ly-route/gateway}
tls_dir=${LY_ROUTE_TLS_DIR:-/etc/ly-route/tls}
mkdir -p "$data_dir" "$tls_dir"

if [ ! -s "$tls_dir/admin.crt" ] || [ ! -s "$tls_dir/admin.key" ]; then
  openssl req -x509 -nodes -newkey rsa:2048 -days 30 \
    -subj "/CN=${LY_ROUTE_TLS_COMMON_NAME:-ly-route-gateway-demo}" \
    -keyout "$tls_dir/admin.key" -out "$tls_dir/admin.crt" >/dev/null 2>&1
fi

export LY_ROUTE_PRODUCT_PROFILE=${LY_ROUTE_PRODUCT_PROFILE:-/etc/ly-route/product-manifest.json}
export LY_ROUTE_API_HOST=${LY_ROUTE_API_HOST:-127.0.0.1}
export LY_ROUTE_API_PORT=${LY_ROUTE_API_PORT:-8080}
export LY_ROUTE_DB_PATH=${LY_ROUTE_DB_PATH:-$data_dir/ly-route.db}
export LY_ROUTE_CONFIG_PATH=${LY_ROUTE_CONFIG_PATH:-$data_dir/config.json}
export LY_ROUTE_ADMIN_USERNAME=${LY_ROUTE_ADMIN_USERNAME:-admin}
export LY_ROUTE_ADMIN_PASSWORD=${LY_ROUTE_ADMIN_PASSWORD:-password}
export LY_ROUTE_FORCE_PASSWORD_CHANGE=${LY_ROUTE_FORCE_PASSWORD_CHANGE:-true}
export LY_ROUTE_SESSION_COOKIE_SECURE=${LY_ROUTE_SESSION_COOKIE_SECURE:-true}
export LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY=${LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY:-false}
export LY_ROUTE_ENABLE_SERVICE_RUNTIME=${LY_ROUTE_ENABLE_SERVICE_RUNTIME:-false}

if [ "${LY_ROUTE_EXTERNAL_CONTROL:-false}" = true ]; then
  exec nginx -g 'daemon off;'
fi

/usr/local/bin/ly-route-control &
control_pid=$!
cleanup() { kill "$control_pid" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

attempt=0
until curl -fsS "http://127.0.0.1:${LY_ROUTE_API_PORT}/api/v1/health" >/dev/null; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 100 ]; then
    echo "ly-route-control did not become ready" >&2
    exit 1
  fi
  sleep 0.1
done

nginx -g 'daemon off;' &
nginx_pid=$!
wait "$nginx_pid"
