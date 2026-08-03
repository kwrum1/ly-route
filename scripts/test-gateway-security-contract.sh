#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

cd "$repo_root"
python3 .sisyphus/security-contracts/validate_security_contracts.py
PYTHONPYCACHEPREFIX=${PYTHONPYCACHEPREFIX:-/tmp/ly-route-pycache} \
	python3 -m py_compile .sisyphus/security-contracts/validate_security_contracts.py

cd "$repo_root/backend"
go test ./internal/httpapi ./internal/runtime/trafficpolicy -run 'TestSecurity|TestCompileConfigBuildsRoutePolicyAndSecurityACL' -count=1

printf 'gateway security ACL, IP-MAC, IP/CIDR threat-list and basic attack contracts passed; DPI, L7, domain and behaviour claims rejected\n'
