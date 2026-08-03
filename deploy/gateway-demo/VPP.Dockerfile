FROM ly-route/vpp-test:25.10

COPY deploy/gateway-demo/artifacts/ly-route-control /usr/local/bin/ly-route-control
COPY deploy/vpp-demo-entrypoint.sh /usr/local/bin/ly-route-vpp-demo
COPY packaging/product-profiles/gateway.json /etc/ly-route/product-manifest.json
COPY deploy/gateway-demo/artifacts/ly_route_security_guard_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_security_guard_plugin.so
COPY deploy/gateway-demo/artifacts/ly_route_smart_qos_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_smart_qos_plugin.so

RUN chmod 0755 /usr/local/bin/ly-route-control /usr/local/bin/ly-route-vpp-demo \
    && mkdir -p /var/lib/ly-route/gateway /etc/ly-route

ENTRYPOINT ["/usr/local/bin/ly-route-vpp-demo"]
