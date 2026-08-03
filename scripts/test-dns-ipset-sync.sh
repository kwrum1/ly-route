#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
sync_script="$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-ipset-sync.py"
command -v python3 >/dev/null

tmp=$(mktemp -d)
cleanup() {
  if [ -n "${server_pid:-}" ]; then kill "$server_pid" 2>/dev/null || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp/bin"
printf '%s\n' 'sync-test-token' > "$tmp/token"
chmod 600 "$tmp/token"
cat > "$tmp/bin/ipset" <<'EOF'
#!/bin/sh
test "$1" = list && test "$2" = lyroute_dns_updates && test "$3" = -t
cat <<'OUT'
Name: lyroute_dns_updates
Type: hash:ip
Members:
203.0.113.53 timeout 42
OUT
EOF
chmod 0755 "$tmp/bin/ipset"
cat > "$tmp/server.py" <<'EOF'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_GET(self):
        if self.path != '/api/v1/dns/policies': self.send_error(404); return
        body = json.dumps({'items': [{'render': {'rules': [{'rule_id': 'updates', 'ipset_name': 'lyroute_dns_updates'}]}}]}).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        if self.path != '/api/v1/internal/dns/ipset-observations': self.send_error(404); return
        if self.headers.get('X-LY-Route-DNS-Sync-Token') != 'sync-test-token': self.send_error(401); return
        size = int(self.headers['Content-Length'])
        with open(sys.argv[1], 'wb') as handle: handle.write(self.rfile.read(size))
        body = b'{}'; self.send_response(202); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)

server = HTTPServer(('127.0.0.1', 0), Handler)
with open(sys.argv[2], 'w') as handle: handle.write(str(server.server_port))
server.handle_request(); server.handle_request()
EOF
python3 "$tmp/server.py" "$tmp/request.json" "$tmp/port" &
server_pid=$!
for _ in $(seq 1 40); do [ -s "$tmp/port" ] && break; sleep .05; done
[ -s "$tmp/port" ]
PATH="$tmp/bin:$PATH" LY_ROUTE_DNS_SYNC_API="http://127.0.0.1:$(cat "$tmp/port")" LY_ROUTE_DNS_SYNC_TOKEN_FILE="$tmp/token" python3 "$sync_script"
wait "$server_pid"
server_pid=
python3 - "$tmp/request.json" <<'EOF'
import json, sys
payload = json.load(open(sys.argv[1], encoding='utf-8'))
assert payload['rule_id'] == 'updates'
assert payload['set_name'] == 'lyroute_dns_updates'
assert payload['members'][0]['ip'] == '203.0.113.53'
assert payload['members'][0]['set_name'] == 'lyroute_dns_updates'
assert payload['members'][0]['expires_at'].endswith('Z')
EOF

echo "DNS ipset sync protocol verification passed"
