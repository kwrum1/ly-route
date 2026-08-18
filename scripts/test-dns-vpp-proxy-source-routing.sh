#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

cat >"$tmpdir/source-routing-test.c" <<EOF
#define main ly_route_dns_proxy_main
#include "$repo_root/packaging/runtime/dns-vpp-proxy.c"
#undef main

static size_t make_query(uint8_t *query, const char *domain) {
    memset(query, 0, 512);
    query[2] = 1;
    query[5] = 1;
    size_t offset = 12;
    const char *label = domain;
    while (*label) {
        const char *dot = strchr(label, '.');
        size_t label_length = dot ? (size_t)(dot - label) : strlen(label);
        if (label_length == 0 || label_length > 63) exit(2);
        query[offset++] = (uint8_t)label_length;
        memcpy(query + offset, label, label_length);
        offset += label_length;
        if (!dot) break;
        label = dot + 1;
    }
    query[offset++] = 0;
    query[offset++] = 0;
    query[offset++] = 1;
    query[offset++] = 0;
    query[offset++] = 1;
    return offset;
}

static void require_port(const uint8_t *query, size_t length, const struct sockaddr *client, int expected) {
    source_routes_next_reload = time(NULL) + 3600;
    int actual = source_route_port(query, length, client);
    if (actual != expected) {
        fprintf(stderr, "source route port = %d, want %d\n", actual, expected);
        exit(1);
    }
}

int main(int argc, char **argv) {
    if (argc != 3) return 2;
    resolve_libc();
    if (load_source_routes(argv[1]) != 0) return 3;

    struct sockaddr_in client4 = {.sin_family = AF_INET};
    struct sockaddr_in outside4 = {.sin_family = AF_INET};
    struct sockaddr_in6 client6 = {.sin6_family = AF_INET6};
    if (inet_pton(AF_INET, "192.0.2.10", &client4.sin_addr) != 1 ||
        inet_pton(AF_INET, "198.51.100.10", &outside4.sin_addr) != 1 ||
        inet_pton(AF_INET6, "2001:db8:1::10", &client6.sin6_addr) != 1) return 4;

    uint8_t query[512];
    size_t length = make_query(query, "updates.example");
    require_port(query, length, (const struct sockaddr *)&client4, 12000);
    require_port(query, length, (const struct sockaddr *)&client6, 12000);
    require_port(query, length, (const struct sockaddr *)&outside4, 53);

    int default_upstream = open_upstream(53, SOCK_DGRAM);
    if (default_upstream < 0) {
        fputs("default SmartDNS upstream could not be opened\n", stderr);
        return 8;
    }
    struct sockaddr_in default_peer = {0};
    socklen_t default_peer_length = sizeof(default_peer);
    char default_peer_address[INET_ADDRSTRLEN] = {0};
    if (getpeername(default_upstream, (struct sockaddr *)&default_peer, &default_peer_length) != 0 ||
        !inet_ntop(AF_INET, &default_peer.sin_addr, default_peer_address, sizeof(default_peer_address)) ||
        strcmp(default_peer_address, "127.0.0.53") != 0 || ntohs(default_peer.sin_port) != 53) {
        fputs("default SmartDNS upstream is not 127.0.0.53:53\n", stderr);
        return 9;
    }
    libc_close(default_upstream);

    length = make_query(query, "cdn.video.example");
    require_port(query, length, (const struct sockaddr *)&client4, 12001);

    uint8_t servfail[12] = {0};
    servfail[3] = 2;
    if (!dns_response_is_servfail(servfail, sizeof(servfail))) {
        fputs("SERVFAIL response was not detected\n", stderr);
        return 6;
    }
    servfail[3] = 0;
    if (dns_response_is_servfail(servfail, sizeof(servfail)) || dns_response_is_servfail(servfail, 11)) {
        fputs("non-SERVFAIL response was detected as retryable\n", stderr);
        return 7;
    }

    uint8_t compressed[64] = {0};
    compressed[2] = 1;
    compressed[5] = 1;
    compressed[12] = 0xc0;
    compressed[13] = 20;
    compressed[14] = 0;
    compressed[15] = 1;
    compressed[16] = 0;
    compressed[17] = 1;
    compressed[20] = 7;
    memcpy(compressed + 21, "updates", 7);
    compressed[28] = 7;
    memcpy(compressed + 29, "example", 7);
    compressed[36] = 0;
    require_port(compressed, 37, (const struct sockaddr *)&client4, 12000);

    if (load_source_routes(argv[2]) == 0) {
        fputs("malformed source route map was accepted\n", stderr);
        return 5;
    }
    puts("DNS VPP proxy source-routing unit verification passed");
    return 0;
}
EOF

cat >"$tmpdir/routes.conf" <<'EOF'
# source-prefix match-kind domain smartdns-port
192.0.2.0/24 exact updates.example 12000
2001:db8:1::/64 exact updates.example 12000
192.0.2.0/24 suffix video.example 12001
EOF
printf '%s\n' '192.0.2.0/24 invalid updates.example 12000' >"$tmpdir/malformed.conf"

cc -O2 -Wall -Wextra -Werror -std=c11 -o "$tmpdir/source-routing-test" "$tmpdir/source-routing-test.c" -ldl
"$tmpdir/source-routing-test" "$tmpdir/routes.conf" "$tmpdir/malformed.conf"

cc -O2 -Wall -Wextra -Werror -std=c11 -o "$tmpdir/dns-vpp-proxy" "$repo_root/packaging/runtime/dns-vpp-proxy.c" -ldl
if LY_ROUTE_DNS_SOURCE_ROUTES="$tmpdir/does-not-exist" "$tmpdir/dns-vpp-proxy" >/dev/null 2>&1; then
    echo 'DNS VPP proxy accepted a missing source route map' >&2
    exit 1
fi
if LY_ROUTE_DNS_SOURCE_ROUTES="$tmpdir/malformed.conf" "$tmpdir/dns-vpp-proxy" >/dev/null 2>&1; then
    echo 'DNS VPP proxy accepted a malformed source route map' >&2
    exit 1
fi

printf '%s\n' 'DNS VPP proxy fail-closed startup verification passed'
