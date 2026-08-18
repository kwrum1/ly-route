#!/usr/bin/env python3
"""Synchronize SmartDNS timed ipsets into Ly Route's fixed-WAN policy API."""

import datetime
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request


API = os.environ.get("LY_ROUTE_DNS_SYNC_API", "http://127.0.0.1:8080")
TOKEN_FILE = os.environ.get("LY_ROUTE_DNS_SYNC_TOKEN_FILE", "/etc/ly-route/dns-sync.token")
API_TIMEOUT_SECONDS = int(os.environ.get("LY_ROUTE_DNS_SYNC_API_TIMEOUT_SECONDS", "30"))


def request(path, data=None, token=None):
    headers = {"Accept": "application/json"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["X-LY-Route-DNS-Sync-Token"] = token
    body = None if data is None else json.dumps(data, separators=(",", ":")).encode()
    with urllib.request.urlopen(
        urllib.request.Request(API + path, data=body, headers=headers),
        timeout=API_TIMEOUT_SECONDS,
    ) as response:
        return json.load(response)


def timed_members(set_name):
    # `ipset list -t` omits the Members section with ipset v7.17. Plain
    # listing still reports each entry's remaining timeout, which is the only
    # value this synchronizer needs to derive its TTL-bound observation.
    result = subprocess.run(["ipset", "list", set_name], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        return []
    members = []
    in_members = False
    now = datetime.datetime.now(datetime.timezone.utc)
    for raw in result.stdout.splitlines():
        line = raw.strip()
        if line == "Members:":
            in_members = True
            continue
        if not in_members or not line:
            continue
        fields = line.split()
        if len(fields) != 3 or fields[1] != "timeout":
            raise RuntimeError("invalid ipset member line: " + line)
        timeout = int(fields[2])
        if timeout <= 0:
            continue
        expires = now + datetime.timedelta(seconds=timeout)
        members.append({"set_name": set_name, "ip": fields[0], "expires_at": expires.replace(microsecond=0).isoformat().replace("+00:00", "Z")})
    return members


def configured_set_names(policies):
    names = set()
    for item in policies.get("items", []):
        for rule in item.get("render", {}).get("rules", []):
            set_name = rule.get("ipset_name", "")
            if set_name:
                names.add(set_name)
    return names


def prepare_ipsets(policies):
    for set_name in sorted(configured_set_names(policies)):
        subprocess.run(
            ["ipset", "create", set_name, "hash:ip", "family", "inet", "timeout", "86400", "-exist"],
            check=True,
        )


def main():
    with open(TOKEN_FILE, "r", encoding="ascii") as handle:
        token = handle.read().strip()
    if not token:
        raise RuntimeError("DNS sync token is empty")
    policies = request("/api/v1/dns/policies")
    if "--prepare" in sys.argv[1:]:
        prepare_ipsets(policies)
        return
    for item in policies.get("items", []):
        for rule in item.get("render", {}).get("rules", []):
            set_name = rule.get("ipset_name", "")
            rule_id = rule.get("rule_id", "")
            if not set_name or not rule_id:
                continue
            request("/api/v1/internal/dns/ipset-observations", {"rule_id": rule_id, "set_name": set_name, "members": timed_members(set_name)}, token)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, urllib.error.URLError, urllib.error.HTTPError) as error:
        print("ly-route DNS ipset sync: " + str(error), file=sys.stderr)
        sys.exit(1)
