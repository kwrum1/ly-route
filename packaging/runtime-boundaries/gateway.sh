#!/bin/sh

# Single source of truth for Gateway runtime packaging gates.
# This file is sourced by the rootfs, disk-image and ISO builders.

# Packages produced or staged as Gateway runtime dependencies. Distribution
# packages are checked in the installed dpkg database; custom packages are also
# required in LY_ROUTE_EXTRA_DEBS_DIR during rootfs construction.
LY_ROUTE_GATEWAY_RUNTIME_PACKAGES="libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk ly-route-vpp-apply ly-route-vpp-pppoe-client ly-route-vpp-smart-qos ly-route-vpp-security-guard ly-route-vpp-dns-intercept ly-route-vpp-pre-nat-route smartdns ly-route-dns-vpp-proxy kea-dhcp4-server xray ipset"
LY_ROUTE_GATEWAY_CUSTOM_RUNTIME_PACKAGES="libvppinfra vpp vpp-plugin-core vpp-plugin-dpdk ly-route-vpp-apply ly-route-vpp-pppoe-client ly-route-vpp-smart-qos ly-route-vpp-security-guard ly-route-vpp-dns-intercept ly-route-vpp-pre-nat-route smartdns ly-route-dns-vpp-proxy xray"

# These are the only custom VPP-loadable plugins in the Gateway product. The
# DNS VPP proxy is a user-space VCL adapter, not a VPP .so; it is gated below
# as a required runtime file and systemd unit.
LY_ROUTE_GATEWAY_VPP_PLUGINS="ly_route_pppoe_client_plugin.so ly_route_smart_qos_plugin.so ly_route_security_guard_plugin.so ly_route_dns_intercept_plugin.so ly_route_pre_nat_route_plugin.so"

# Files that must survive rootfs -> disk image -> ISO installation.
LY_ROUTE_GATEWAY_RUNTIME_FILES_COMMON="/usr/bin/vpp /usr/bin/vppctl /usr/lib/ly-route/vpp-apply /usr/lib/ly-route/ly-route-pppoe-client /usr/lib/ly-route/ly-route-dns-vpp-proxy /usr/lib/ly-route/ly-route-dns-vpp-proxy-v6 /usr/lib/ly-route/dns-vpp-v6-namespace-apply /usr/lib/ly-route/dns-vpp-session-apply /usr/sbin/smartdns /usr/sbin/kea-dhcp4 /usr/bin/xray /usr/sbin/ipset"
LY_ROUTE_GATEWAY_RUNTIME_UNITS="vpp.service smartdns.service ly-route-dns-vpp-proxy.service ly-route-dns-vpp-proxy-v6.service kea-dhcp4-server.service xray.service ly-route-pppoe.target"
