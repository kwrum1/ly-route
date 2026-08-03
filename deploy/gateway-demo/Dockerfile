FROM ly-flow/frontend:bugfix-20260801

COPY deploy/gateway-demo/artifacts/admin /opt/ly-route/admin
COPY deploy/gateway-demo/artifacts/ly-route-control /usr/local/bin/ly-route-control
COPY deploy/gateway-demo/nginx.conf /etc/nginx/conf.d/default.conf
COPY deploy/gateway-demo/entrypoint.sh /usr/local/bin/ly-route-gateway-demo
COPY packaging/product-profiles/gateway.json /etc/ly-route/product-manifest.json

RUN chmod 0755 /usr/local/bin/ly-route-control /usr/local/bin/ly-route-gateway-demo && mkdir -p /var/lib/ly-route/gateway /etc/ly-route/tls

EXPOSE 443
HEALTHCHECK --interval=15s --timeout=3s --retries=5 CMD curl -kfsS https://127.0.0.1/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/ly-route-gateway-demo"]
