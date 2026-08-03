FROM ly-flow/frontend:bugfix-20260801

COPY deploy/orchestrator-demo/artifacts/admin /opt/ly-route/admin
COPY deploy/orchestrator-demo/artifacts/ly-route-orchestrator /usr/local/bin/ly-route-orchestrator
COPY deploy/orchestrator-demo/nginx.conf /etc/nginx/conf.d/default.conf
COPY deploy/orchestrator-demo/entrypoint.sh /usr/local/bin/ly-route-orchestrator-demo
COPY deploy/orchestrator-demo/vppctl-demo.sh /usr/local/bin/ly-route-vppctl-demo
COPY packaging/product-profiles/orchestrator.json /etc/ly-route/product-manifest.json

RUN chmod 0755 /usr/local/bin/ly-route-orchestrator /usr/local/bin/ly-route-orchestrator-demo /usr/local/bin/ly-route-vppctl-demo \
    && mkdir -p /var/lib/ly-route/orchestrator /etc/ly-route/tls

EXPOSE 443
HEALTHCHECK --interval=15s --timeout=3s --retries=5 CMD curl -kfsS https://127.0.0.1/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/ly-route-orchestrator-demo"]
