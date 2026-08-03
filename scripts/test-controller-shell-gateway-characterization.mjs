import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const appPath = join(repoRoot, "frontend/gateway/app.js");
const expectedInventory = [
  ["overview", "系统概况", "system/system_overview", "系统概况"],
  ["overview", "系统概况", "dashboard/dashboard", "流量概况"],
  ["overview", "系统概况", "monitor/ipobj_main", "在线用户"],
  ["overview", "系统概况", "monitor/flow_topn", "Top连接"],
  ["overview", "系统概况", "monitor/domain_topn", "Top域名"],
  ["network", "网络设置", "monitor/interface_list", "网卡设置"],
  ["network", "网络设置", "network/proxy_main", "LAN/WAN"],
  ["network", "网络设置", "network/wangroup_manager", "WAN群组"],
  ["network", "网络设置", "route/route_policy_main", "路由/NAT"],
  ["network", "网络设置", "route/portmap_list", "端口映射"],
  ["network", "网络设置", "route/dnspolicy_main", "DNS管控"],
  ["network", "网络设置", "network/dhcpsvr_main", "DHCP服务"],
  ["behavior", "行为管理", "flowcontrol/flowct_main", "流量控制"],
  ["object", "对象管理", "object/urlgrp_list", "域名管理"],
  ["object", "对象管理", "object/iptab_list", "IP管理"],
  ["system", "系统维护", "system/webuser_main", "系统用户管理"],
  ["system", "系统维护", "system/sys_config", "配置管理"],
  ["system", "系统维护", "system/runtime_ops", "运行态操作"],
  ["system", "系统维护", "system/firmware_update", "固件更新"],
];

// Given the settled Gateway source before profile isolation.
const source = await readFile(appPath, "utf8");
const start = source.indexOf("const sections");
const boundary = source.indexOf("const pageMap");
assert.notEqual(start, -1, "Gateway route registry is missing");
assert.notEqual(boundary, -1, "Gateway route registry boundary is missing");
const context = {};
vm.runInNewContext(`${source.slice(start, boundary)}globalThis.sections = sections;`, context);

// When the route registry is enumerated.
const inventory = JSON.parse(JSON.stringify(context.sections)).flatMap((section) =>
  section.pages.map(([route, label]) => [section.id, section.title, route, label]),
);

// Then the current Gateway menu and route inventory is exact.
assert.deepEqual(inventory, expectedInventory);
console.log(`Gateway route/menu characterization passed (${inventory.length} routes).`);
