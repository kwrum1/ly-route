FROM ly-route/vpp-test:25.10

COPY deploy/orchestrator-demo/artifacts/ly-route-orchestrator /usr/local/bin/ly-route-orchestrator
COPY deploy/vpp-demo-entrypoint.sh /usr/local/bin/ly-route-vpp-demo
COPY packaging/product-profiles/orchestrator.json /etc/ly-route/product-manifest.json
COPY deploy/orchestrator-demo/artifacts/ly_route_orchestrator_plugin.so /usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_orchestrator_plugin.so

RUN chmod 0755 /usr/local/bin/ly-route-orchestrator /usr/local/bin/ly-route-vpp-demo \
    && mkdir -p /var/lib/ly-route/orchestrator /etc/ly-route

ENTRYPOINT ["/usr/local/bin/ly-route-vpp-demo"]
