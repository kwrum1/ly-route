FROM ly-route/vpp-test:25.10

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        iproute2 ppp pppoe iputils-ping \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/gateway-e2e/pppoe-server-entrypoint.sh /usr/local/bin/pppoe-server-entrypoint
COPY deploy/gateway-e2e/artifacts/dhcp6-pd-fixture /usr/local/bin/dhcp6-pd-fixture
RUN chmod 0755 /usr/local/bin/pppoe-server-entrypoint /usr/local/bin/dhcp6-pd-fixture
ENTRYPOINT ["/usr/local/bin/pppoe-server-entrypoint"]
