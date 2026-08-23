#define _GNU_SOURCE

#include <arpa/inet.h>
#include <ctype.h>
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

typedef int (*socket_fn)(int, int, int);
typedef int (*connect_fn)(int, const struct sockaddr *, socklen_t);
typedef ssize_t (*sendto_fn)(int, const void *, size_t, int, const struct sockaddr *, socklen_t);
typedef ssize_t (*recvfrom_fn)(int, void *, size_t, int, struct sockaddr *, socklen_t *);
typedef int (*close_fn)(int);
typedef int (*poll_fn)(struct pollfd *, nfds_t, int);

static socket_fn libc_socket;
static connect_fn libc_connect;
static sendto_fn libc_sendto;
static recvfrom_fn libc_recvfrom;
static close_fn libc_close;
static poll_fn libc_poll;
static volatile sig_atomic_t stopping;
static int first_udp_response = 1;

#define MAX_SOURCE_ROUTES 4096
#define MAX_DNS_NAME 255
#define UPSTREAM_ATTEMPTS 3
#define SERVFAIL_RETRY_DELAY_US 1200000
struct source_route {
    int family;
    uint8_t address[16];
    unsigned int prefix_length;
    int match_suffix;
    char domain[MAX_DNS_NAME + 1];
    uint16_t port;
};

static struct source_route source_routes[MAX_SOURCE_ROUTES];
static size_t source_route_count;
static int source_routes_loaded;
static time_t source_routes_next_reload;

static int parse_decimal(const char *text, unsigned int maximum, unsigned int *value) {
    unsigned int parsed = 0;
    if (!text || !*text) return -1;
    for (const unsigned char *cursor = (const unsigned char *)text; *cursor; cursor++) {
        if (!isdigit(*cursor) || parsed > (maximum - (unsigned int)(*cursor - '0')) / 10U) return -1;
        parsed = parsed * 10U + (unsigned int)(*cursor - '0');
    }
    *value = parsed;
    return 0;
}

static void resolve_libc(void) {
    void *handle = dlopen("libc.so.6", RTLD_NOW | RTLD_LOCAL);
    if (!handle) {
        fprintf(stderr, "ly-route-dns-vpp-proxy: libc could not be loaded: %s\n", dlerror());
        exit(EXIT_FAILURE);
	}
	libc_socket = (socket_fn)dlsym(handle, "socket");
	libc_connect = (connect_fn)dlsym(handle, "connect");
    libc_sendto = (sendto_fn)dlsym(handle, "sendto");
    libc_recvfrom = (recvfrom_fn)dlsym(handle, "recvfrom");
    libc_close = (close_fn)dlsym(handle, "close");
	libc_poll = (poll_fn)dlsym(handle, "poll");
	if (!libc_socket || !libc_connect || !libc_sendto || !libc_recvfrom || !libc_close || !libc_poll) {
        fprintf(stderr, "ly-route-dns-vpp-proxy: libc socket symbols unavailable\n");
        exit(EXIT_FAILURE);
    }
}

static void on_signal(int signo) {
    (void)signo;
    stopping = 1;
}

static int bind_listener(int family, int type, uint16_t port) {
    struct sockaddr_storage address = {0};
    socklen_t address_length;
    if (family == AF_INET) {
        struct sockaddr_in *v4 = (struct sockaddr_in *)&address;
        v4->sin_family = AF_INET;
        v4->sin_port = htons(port);
        v4->sin_addr.s_addr = htonl(INADDR_ANY);
        address_length = sizeof(*v4);
    } else {
        struct sockaddr_in6 *v6 = (struct sockaddr_in6 *)&address;
        v6->sin6_family = AF_INET6;
        v6->sin6_port = htons(port);
        v6->sin6_addr = in6addr_any;
        address_length = sizeof(*v6);
    }
    int fd = socket(family, type, 0);
    if (fd < 0 || bind(fd, (const struct sockaddr *)&address, address_length) < 0) {
        if (fd >= 0) close(fd);
        return -1;
    }
    if (type == SOCK_STREAM && listen(fd, 128) < 0) {
        close(fd);
        return -1;
    }
	int flags = fcntl(fd, F_GETFL, 0);
	if (flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0) {
		close(fd);
		return -1;
	}
    return fd;
}

static int parse_source_prefix(const char *value, struct source_route *route) {
    char text[INET6_ADDRSTRLEN + 4];
    const char *slash = strchr(value, '/');
    if (!slash || (size_t)(slash - value) >= sizeof(text)) return -1;
    memcpy(text, value, (size_t)(slash - value));
    text[slash - value] = '\0';
    route->family = strchr(text, ':') ? AF_INET6 : AF_INET;
    unsigned int maximum = route->family == AF_INET ? 32U : 128U;
    unsigned int prefix;
    if (parse_decimal(slash + 1, maximum, &prefix) < 0 || inet_pton(route->family, text, route->address) != 1) return -1;
    route->prefix_length = prefix;
    return 0;
}

static int load_source_routes(const char *path) {
    FILE *file = fopen(path, "r");
    if (!file) return -1;
    struct source_route staged[MAX_SOURCE_ROUTES];
    size_t count = 0;
    char line[768];
    while (fgets(line, sizeof(line), file)) {
        char *cursor = line;
        while (isspace((unsigned char)*cursor)) cursor++;
        if (*cursor == '\0' || *cursor == '#') continue;
        char *save = NULL;
        char *prefix = strtok_r(cursor, " \t\r\n", &save);
        char *match_kind = strtok_r(NULL, " \t\r\n", &save);
        char *domain = strtok_r(NULL, " \t\r\n", &save);
        char *port_text = strtok_r(NULL, " \t\r\n", &save);
        char *extra = strtok_r(NULL, " \t\r\n", &save);
        unsigned int port;
        if (count >= MAX_SOURCE_ROUTES || !prefix || !match_kind || !domain || !port_text || extra ||
            parse_decimal(port_text, 65535, &port) < 0 || port < 1024 || parse_source_prefix(prefix, &staged[count]) < 0) {
            fclose(file);
            return -1;
        }
        if (strcmp(match_kind, "exact") == 0) {
            staged[count].match_suffix = 0;
        } else if (strcmp(match_kind, "suffix") == 0) {
            staged[count].match_suffix = 1;
        } else {
            fclose(file);
            return -1;
        }
        size_t length = strlen(domain);
        if (length == 0 || length > MAX_DNS_NAME) {
            fclose(file);
            return -1;
        }
        for (size_t i = 0; i < length; i++) domain[i] = (char)tolower((unsigned char)domain[i]);
        memcpy(staged[count].domain, domain, length + 1);
        staged[count].port = (uint16_t)port;
        count++;
    }
    if (ferror(file)) {
        fclose(file);
        return -1;
    }
    fclose(file);
    memcpy(source_routes, staged, count * sizeof(staged[0]));
    source_route_count = count;
    source_routes_loaded = 1;
    return 0;
}

static int refresh_source_routes(void) {
    const char *path = getenv("LY_ROUTE_DNS_SOURCE_ROUTES");
    if (!path || !*path) path = "/etc/ly-route/dns-source-routes.conf";
    time_t now = time(NULL);
    if (source_routes_loaded && now < source_routes_next_reload) return 0;
    source_routes_next_reload = now + 1;
    return load_source_routes(path);
}

static int parse_query_domain(const uint8_t *query, size_t length, char *domain) {
    if (length < 17 || (query[4] == 0 && query[5] == 0)) return -1;
    size_t offset = 12;
    size_t question_end = 0;
    size_t written = 0;
    size_t jumps = 0;
    int jumped = 0;
    while (offset < length && jumps++ <= length) {
        uint8_t label = query[offset++];
        if (label == 0) {
            if (!jumped) question_end = offset;
            break;
        }
        if ((label & 0xc0) == 0xc0) {
            if (offset >= length) return -1;
            size_t pointer = ((size_t)(label & 0x3f) << 8) | query[offset++];
            if (pointer >= length) return -1;
            if (!jumped) question_end = offset;
            offset = pointer;
            jumped = 1;
            continue;
        }
        if ((label & 0xc0) != 0 || label > 63 || offset + label > length) return -1;
        if (written != 0) {
            if (written >= MAX_DNS_NAME) return -1;
            domain[written++] = '.';
        }
        if (written + label > MAX_DNS_NAME) return -1;
        for (uint8_t i = 0; i < label; i++) domain[written++] = (char)tolower(query[offset++]);
    }
    if (written == 0 || question_end == 0 || question_end + 4 > length || jumps > length) return -1;
    domain[written] = '\0';
    return 0;
}

static int source_matches(const struct source_route *route, const struct sockaddr *client) {
    const uint8_t *address;
    if (route->family == AF_INET && client->sa_family == AF_INET) {
        address = (const uint8_t *)&((const struct sockaddr_in *)client)->sin_addr;
    } else if (route->family == AF_INET6 && client->sa_family == AF_INET6) {
        address = (const uint8_t *)&((const struct sockaddr_in6 *)client)->sin6_addr;
    } else {
        return 0;
    }
    unsigned int full = route->prefix_length / 8;
    unsigned int remainder = route->prefix_length % 8;
    if (full && memcmp(address, route->address, full) != 0) return 0;
    if (!remainder) return 1;
    uint8_t mask = (uint8_t)(0xffU << (8U - remainder));
    return (address[full] & mask) == (route->address[full] & mask);
}

static int domain_matches(const struct source_route *route, const char *domain) {
    if (strcmp(domain, route->domain) == 0) return 1;
    if (!route->match_suffix) return 0;
    size_t domain_length = strlen(domain);
    size_t suffix_length = strlen(route->domain);
    return domain_length > suffix_length && domain[domain_length - suffix_length - 1] == '.' &&
           strcmp(domain + domain_length - suffix_length, route->domain) == 0;
}

static int source_route_port(const uint8_t *query, size_t length, const struct sockaddr *client) {
    char domain[MAX_DNS_NAME + 1];
    if (refresh_source_routes() < 0 || parse_query_domain(query, length, domain) < 0) return -1;
    // The normal SmartDNS listener is the loopback stub at 127.0.0.53:53.
    // Source-route entries use dedicated 127.0.0.1 ports and override this.
    int port = 53;
    for (size_t i = 0; i < source_route_count; i++) {
        if (domain_matches(&source_routes[i], domain) && source_matches(&source_routes[i], client)) {
            port = source_routes[i].port;
            break;
        }
    }
    if (getenv("LY_ROUTE_DNS_ROUTE_DEBUG")) {
        char address[INET6_ADDRSTRLEN] = "unknown";
        const void *source = NULL;
        if (client->sa_family == AF_INET) source = &((const struct sockaddr_in *)client)->sin_addr;
        if (client->sa_family == AF_INET6) source = &((const struct sockaddr_in6 *)client)->sin6_addr;
        if (source) (void)inet_ntop(client->sa_family, source, address, sizeof(address));
        fprintf(stderr, "ly-route-dns-vpp-proxy: source=%s domain=%s port=%d\n", address, domain, port);
        fflush(stderr);
    }
    return port;
}

static int dns_response_is_servfail(const uint8_t *response, size_t length) {
    return length >= 12 && (response[3] & 0x0f) == 2;
}

static int open_upstream(uint16_t port, int type) {
	struct sockaddr_in address = {
		.sin_family = AF_INET,
		.sin_port = htons(port),
	};
	const char *host = port == 53 ? "127.0.0.53" : "127.0.0.1";
	if (inet_pton(AF_INET, host, &address.sin_addr) != 1) return -1;
	int fd = libc_socket(AF_INET, type, 0);
	if (fd < 0 || libc_connect(fd, (const struct sockaddr *)&address, sizeof(address)) < 0) {
		int saved_errno = errno;
		if (fd >= 0) libc_close(fd);
		if (getenv("LY_ROUTE_DNS_ROUTE_DEBUG")) {
			fprintf(stderr, "ly-route-dns-vpp-proxy: upstream %s port=%u failed: %s\n",
			        type == SOCK_STREAM ? "tcp" : "udp", (unsigned int)port, strerror(saved_errno));
			fflush(stderr);
		}
		return -1;
    }
    return fd;
}

static int relay_udp(int listener) {
    uint8_t query[65536];
    uint8_t answer[65536];
    struct sockaddr_storage client;
    socklen_t client_length = sizeof(client);
    ssize_t query_length = recvfrom(listener, query, sizeof(query), 0,
                                    (struct sockaddr *)&client, &client_length);
    if (query_length < 12) return 0;

    int port = source_route_port(query, (size_t)query_length, (const struct sockaddr *)&client);
    if (port < 0) return 0;
    for (int attempt = 0; attempt < UPSTREAM_ATTEMPTS; attempt++) {
		int upstream = open_upstream((uint16_t)port, SOCK_DGRAM);
		if (upstream < 0) continue;
		ssize_t sent = libc_sendto(upstream, query, (size_t)query_length, 0, NULL, 0);
		if (sent != query_length) {
			libc_close(upstream);
			continue;
		}
		struct pollfd wait_for_answer = { .fd = upstream, .events = POLLIN };
		if (libc_poll(&wait_for_answer, 1, 3000) <= 0) {
			libc_close(upstream);
			continue;
		}
		ssize_t answer_length = libc_recvfrom(upstream, answer, sizeof(answer), 0, NULL, NULL);
		libc_close(upstream);
		if (answer_length < 12) continue;
		if (dns_response_is_servfail(answer, (size_t)answer_length) && attempt + 1 < UPSTREAM_ATTEMPTS) {
			usleep(SERVFAIL_RETRY_DELAY_US);
			continue;
		}
		(void)sendto(listener, answer, (size_t)answer_length, 0,
		             (const struct sockaddr *)&client, client_length);
		if (first_udp_response) {
			first_udp_response = 0;
			usleep(10000);
			(void)sendto(listener, answer, (size_t)answer_length, 0,
			             (const struct sockaddr *)&client, client_length);
		}
		return 0;
	}
    return 0;
}

static int read_full(int fd, void *buffer, size_t length) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t count = recv(fd, (uint8_t *)buffer + offset, length - offset, 0);
        if (count <= 0) return -1;
        offset += (size_t)count;
    }
    return 0;
}

static int write_full(int fd, const void *buffer, size_t length) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t count = send(fd, (const uint8_t *)buffer + offset, length - offset, 0);
        if (count <= 0) return -1;
        offset += (size_t)count;
    }
    return 0;
}

static int read_full_libc(int fd, void *buffer, size_t length) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t count = libc_recvfrom(fd, (uint8_t *)buffer + offset, length - offset, 0, NULL, NULL);
        if (count <= 0) return -1;
        offset += (size_t)count;
    }
    return 0;
}

static int write_full_libc(int fd, const void *buffer, size_t length) {
    size_t offset = 0;
    while (offset < length) {
        ssize_t count = libc_sendto(fd, (const uint8_t *)buffer + offset, length - offset, 0, NULL, 0);
        if (count <= 0) return -1;
        offset += (size_t)count;
    }
    return 0;
}

static void relay_tcp_client(int client, const struct sockaddr *peer) {
    uint8_t length_bytes[2];
    uint8_t query[65537];
    uint8_t answer[65537];
    if (read_full(client, length_bytes, sizeof(length_bytes)) < 0) return;
    size_t query_length = ((size_t)length_bytes[0] << 8) | length_bytes[1];
    if (query_length < 12 || query_length > 65535) return;
    query[0] = length_bytes[0];
    query[1] = length_bytes[1];
    if (read_full(client, query + 2, query_length) < 0) return;

    int port = source_route_port(query + 2, query_length, peer);
    if (port < 0) return;

	for (int attempt = 0; attempt < UPSTREAM_ATTEMPTS; attempt++) {
		int upstream = open_upstream((uint16_t)port, SOCK_STREAM);
		if (upstream < 0) continue;
		if (write_full_libc(upstream, query, query_length + 2) < 0) {
			libc_close(upstream);
			continue;
		}
		struct pollfd wait_for_answer = { .fd = upstream, .events = POLLIN };
		if (libc_poll(&wait_for_answer, 1, 3000) <= 0 ||
			read_full_libc(upstream, answer, sizeof(length_bytes)) < 0) {
			libc_close(upstream);
			continue;
		}
		size_t answer_length = ((size_t)answer[0] << 8) | answer[1];
		if (answer_length < 12 || answer_length > 65535 || read_full_libc(upstream, answer + 2, answer_length) < 0) {
			libc_close(upstream);
			continue;
		}
		libc_close(upstream);
		if (dns_response_is_servfail(answer + 2, answer_length) && attempt + 1 < UPSTREAM_ATTEMPTS) {
			usleep(SERVFAIL_RETRY_DELAY_US);
			continue;
		}
		(void)write_full(client, answer, answer_length + 2);
		return;
	}
}

int main(void) {
    resolve_libc();
    if (refresh_source_routes() < 0) {
        fprintf(stderr, "ly-route-dns-vpp-proxy: source route configuration is unavailable\n");
        return EXIT_FAILURE;
    }
    signal(SIGTERM, on_signal);
    signal(SIGINT, on_signal);
    const char *family = getenv("LY_ROUTE_DNS_FAMILY");
    int enable_v4 = !family || !*family || strcmp(family, "dual") == 0 || strcmp(family, "ipv4") == 0;
    int enable_v6 = !family || !*family || strcmp(family, "dual") == 0 || strcmp(family, "ipv6") == 0;
    if (!enable_v4 && !enable_v6) {
        fprintf(stderr, "ly-route-dns-vpp-proxy: LY_ROUTE_DNS_FAMILY must be ipv4, ipv6, or dual\n");
        return EXIT_FAILURE;
    }
    int udp4 = enable_v4 ? bind_listener(AF_INET, SOCK_DGRAM, 53) : -1;
    int tcp4 = enable_v4 ? bind_listener(AF_INET, SOCK_STREAM, 53) : -1;
    int udp6 = enable_v6 ? bind_listener(AF_INET6, SOCK_DGRAM, 53) : -1;
    int tcp6 = enable_v6 ? bind_listener(AF_INET6, SOCK_STREAM, 53) : -1;
	if ((enable_v4 && (udp4 < 0 || tcp4 < 0)) || (enable_v6 && (udp6 < 0 || tcp6 < 0))) {
		fprintf(stderr, "ly-route-dns-vpp-proxy: failed to bind VPP DNS service\n");
		return EXIT_FAILURE;
	}
	int listeners[] = {udp4, tcp4, udp6, tcp6};
	while (!stopping) {
		for (int role = 0; role < 4; role++) {
			if (listeners[role] < 0) continue;
			if ((role % 2) == 0) {
				relay_udp(listeners[role]);
				continue;
			}
			struct sockaddr_storage peer = {0};
			socklen_t peer_length = sizeof(peer);
			int client = accept(listeners[role], (struct sockaddr *)&peer, &peer_length);
			if (client >= 0) {
				relay_tcp_client(client, (const struct sockaddr *)&peer);
				close(client);
			}
		}
		usleep(1000);
	}
    if (udp4 >= 0) close(udp4);
    if (tcp4 >= 0) close(tcp4);
    if (udp6 >= 0) close(udp6);
    if (tcp6 >= 0) close(tcp6);
    return 0;
}
