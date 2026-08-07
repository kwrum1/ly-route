#!/usr/bin/env python3
import json
import os
import re
import stat
import sys


TOKEN = re.compile(r"^[A-Za-z0-9_.:-]+$")
VPP_INTERFACE = re.compile(r"^[A-Za-z0-9_.:/-]+$")
PCI = re.compile(r"^[0-9A-Fa-f]{4}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}\.[0-7]$")


def fail(message):
    print(message, file=sys.stderr)
    raise SystemExit(1)


if len(sys.argv) != 3:
    fail("usage: active-dpdk-state.py ACTIVE_STATE LINUX_INTERFACE")

path, interface_name = sys.argv[1:]
if not TOKEN.fullmatch(interface_name):
    fail("unsafe Linux interface name")

try:
    metadata = os.lstat(path)
except OSError as error:
    fail(f"cannot inspect active dataplane state: {error}")
if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0:
    fail("active dataplane state must be a root-owned regular file")
if stat.S_IMODE(metadata.st_mode) & 0o077:
    fail("active dataplane state must not be accessible by group or other")

try:
    with open(path, "r", encoding="utf-8") as source:
        state = json.load(source)
except (OSError, UnicodeError, json.JSONDecodeError) as error:
    fail(f"cannot parse active dataplane state: {error}")

path_state = state.get("path")
if not isinstance(path_state, dict):
    fail("active dataplane path is missing")
if path_state.get("tier") != "vpp_dpdk":
    fail("active dataplane path is not DPDK")
attachments = path_state.get("attachments")
if not isinstance(attachments, list) or not attachments:
    fail("active dataplane attachments are missing")

matches = [item for item in attachments if isinstance(item, dict) and item.get("linux_interface") == interface_name]
if len(matches) != 1:
    fail("active dataplane attachment does not uniquely match interface")
attachment = matches[0]
values = [
    attachment.get("pci_address"),
    attachment.get("kernel_driver"),
    attachment.get("iommu_group"),
    attachment.get("vpp_interface"),
	attachment.get("mode"),
]
if not PCI.fullmatch(values[0] or ""):
    fail("active dataplane PCI address is invalid")
if not TOKEN.fullmatch(values[1] or "") or not TOKEN.fullmatch(values[2] or ""):
    fail("active dataplane attachment identity is invalid")
if not VPP_INTERFACE.fullmatch(values[3] or ""):
    fail("active VPP interface identity is invalid")
if values[4] not in {"vfio_pci", "uio_pci_generic"}:
    fail("active DPDK binding mode is invalid")

print("|".join(values))
