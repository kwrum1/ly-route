#!/bin/sh
set -eu
umask 077

if [ "$#" -ne 3 ]; then
  echo "usage: migrate-control-env.sh CONTROL_ENV MANAGEMENT_INTERFACE MANAGEMENT_CIDR" >&2
  exit 2
fi

control_env=$1
management_interface=$2
management_cidr=$3
control_env_tmp="$control_env.tmp.$$"
cleanup() { rm -f "$control_env_tmp"; }
trap cleanup EXIT INT TERM

if [ -f "$control_env" ]; then
  grep -v \
    -e '^LY_ROUTE_MANAGEMENT_INTERFACE=' \
    -e '^LY_ROUTE_LAN_INTERFACE=' \
    -e '^LY_ROUTE_LAN_CIDR=' \
    "$control_env" > "$control_env_tmp" || true
else
  : > "$control_env_tmp"
fi
printf 'LY_ROUTE_MANAGEMENT_INTERFACE=%s\n' "$management_interface" >> "$control_env_tmp"
printf 'LY_ROUTE_LAN_CIDR=%s\n' "$management_cidr" >> "$control_env_tmp"
chmod 0600 "$control_env_tmp"
mv -f "$control_env_tmp" "$control_env"
