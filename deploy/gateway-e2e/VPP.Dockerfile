FROM ly-route-gateway-vpp-demo:latest

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        iproute2 ppp pppoe procps kea-dhcp4-server \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/gateway-demo/artifacts/ly-route-control /usr/local/bin/ly-route-control
COPY deploy/gateway-e2e/artifacts/ly-route-pppoe-client /usr/local/bin/ly-route-pppoe-client
COPY deploy/gateway-e2e/artifacts/ly_route_pppoe_client_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_pppoe_client_plugin.so
COPY deploy/gateway-e2e/artifacts/dhcp_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/dhcp_plugin.so
COPY deploy/gateway-e2e/artifacts/af_packet_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/af_packet_plugin.so
COPY deploy/gateway-e2e/systemctl-e2e /usr/local/bin/systemctl
COPY deploy/gateway-e2e/vppctl-e2e /usr/local/bin/ly-route-vppctl
COPY deploy/gateway-e2e/vpp-demo-e2e /usr/local/bin/ly-route-vpp-demo
COPY deploy/gateway-e2e/entrypoint.sh /usr/local/bin/gateway-e2e-entrypoint
RUN chmod 0755 /usr/local/bin/ly-route-pppoe-client /usr/local/bin/ly-route-vppctl /usr/local/bin/ly-route-vpp-demo /usr/local/bin/systemctl /usr/local/bin/gateway-e2e-entrypoint
ENTRYPOINT ["/usr/local/bin/gateway-e2e-entrypoint"]
