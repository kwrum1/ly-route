FROM ly-route/vpp-test:25.10

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        iproute2 iputils-ping ndisc6 procps \
    && rm -rf /var/lib/apt/lists/*

ENTRYPOINT []
