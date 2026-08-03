import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createRequire } from "node:module";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { startOrchestratorFixture } from "./fixtures/orchestrator-ui-fixture.mjs";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");
const run = promisify(execFile);
const repoRoot = join(import.meta.dirname, "..");
const fixturePath = join(repoRoot, "backend/internal/orchestratorapi/testdata/topology-v1.json");
const evidenceDir = process.env.TASK18_EVIDENCE_DIR || join(repoRoot, ".omo/evidence/task-18-ui");
const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "mobile", width: 390, height: 844 },
];

async function resetFixture(page, initial = "empty", mode = "normal") {
  const response = await page.request.post("/__fixture__/state", { data: { initial, mode } });
  assert.equal(response.ok(), true, "fixture reset must succeed");
}

async function setMode(page, mode) {
  const response = await page.request.post("/__fixture__/mode", { data: { mode } });
  assert.equal(response.ok(), true, `fixture mode ${mode} must be accepted`);
}

async function configureRole(page, role, port) {
  const trigger = page.getByRole("button", { name: `配置 ${role.toUpperCase()}` });
  await trigger.click();
  await page.getByRole("combobox", { name: "连接方式" }).selectOption("port");
  await page.getByRole("combobox", { name: "物理网卡" }).selectOption(port);
  await page.getByRole("button", { name: "确定" }).click();
}

async function configureBond(page, role, members) {
  await page.getByRole("button", { name: `配置 ${role.toUpperCase()}` }).click();
  await page.getByRole("combobox", { name: "连接方式" }).selectOption("bond");
  assert.equal(await page.locator('[data-role-port] option[value="eth0"]').count(), 0, "management port must not be selectable");
  for (const member of members) await page.getByRole("checkbox", { name: new RegExp(`^${member}`) }).check();
  await page.getByRole("button", { name: "确定" }).click();
}

async function createGroup(page, name, lanPort, wanPort) {
  await page.getByRole("button", { name: "新建编排组" }).click();
  await page.getByRole("textbox", { name: "组名称" }).fill(name);
  await page.getByRole("combobox", { name: "LAN 侧端口" }).selectOption(lanPort);
  await page.getByRole("combobox", { name: "WAN 侧端口" }).selectOption(wanPort);
  await page.getByRole("button", { name: "确定" }).click();
}

async function assertSharedManagementFlow(page, capture) {
  await page.getByRole("button", { name: "配置管理" }).click();
  await page.getByRole("radio", { name: "与 LAN 共享" }).check();
  await page.getByRole("textbox", { name: "IP/掩码" }).fill("10.10.10.254/24");
  await page.getByRole("textbox", { name: "网关" }).fill("10.10.10.254");
  await page.getByText("确认修改管理访问地址").click();
  const saved = page.waitForResponse((response) => response.request().method() === "POST" && response.url().includes("/api/v1/management/network"));
  await page.getByRole("button", { name: "保存管理口" }).click();
  const managementResponse = await saved;
  const managementRequest = managementResponse.request().postDataJSON();
  assert.equal(managementRequest.mode, "shared_lan", "Orchestrator must submit shared_lan explicitly");
  await page.getByText("管理口配置已从 API 回读确认").waitFor();
  await page.getByText("应用后访问 https://10.10.10.254/").waitFor();

  await page.getByRole("button", { name: "网卡设置" }).click();
  await configureRole(page, "lan", "eth0");
  await configureRole(page, "wan", "eth1");
  const topologySaved = page.waitForResponse((response) => response.request().method() === "PUT" && response.url().includes("/api/v1/orchestrator/topology"));
  await page.getByRole("button", { name: "保存网卡设置" }).click();
  const topologyRequest = (await topologySaved).request().postDataJSON();
  assert.equal(topologyRequest.management_shared, true, "shared mode must be carried into topology validation");
  assert.equal(topologyRequest.interfaces.find((item) => item.role === "lan").port, "eth0", "management interface must be reusable only as LAN");
  await page.getByText("LAN + 管理共享").waitFor();
  await capture("shared-management");

  await page.getByRole("button", { name: "流量编排" }).click();
  await page.getByRole("button", { name: "新增策略明细" }).click();
  await page.locator("[data-policy-group]").fill("default-policy");
  await page.locator("[data-policy-rule]").fill("office-direct");
  await page.locator("[data-policy-source-mode]").selectOption("ip_group");
  await page.locator("[data-policy-source-group]").selectOption({ label: "办公终端" });
  await page.locator("[data-policy-action]").selectOption("direct");
  const policySaved = page.waitForResponse((response) => response.request().method() === "PUT" && response.url().includes("/api/v1/orchestrator/policy"), { timeout: 15000 }).catch((error) => ({ error }));
  await page.locator("#modalOk").click();
  const policyResponse = await policySaved;
  if (policyResponse?.error) {
    const diagnostics = await page.locator(".orchestrator-notice, #modal").allTextContents();
    throw new Error(`policy save request missing: ${JSON.stringify(diagnostics)}`, { cause: policyResponse.error });
  }
  const policyRequest = policyResponse.request().postDataJSON();
  const sourceObjectID = policyRequest.policy_groups[0].rules[0].match.sources[0];
  const officeObject = policyRequest.ip_objects.find((item) => item.id === sourceObjectID);
  assert.deepEqual(officeObject.prefixes, ["192.168.10.0/24", "192.168.20.10-192.168.20.20"], "selected IP group must be embedded for backend compilation");
  await page.getByText("编排策略已保存并完成 API 回读").waitFor();
  await page.getByText("office-direct").waitFor();
  await capture("policy-ip-group");
}

async function assertRetainedProductManagement(page, capture) {
  await page.getByRole("button", { name: "IP管理" }).click();
  await page.getByRole("button", { name: "新增 IP 组" }).click();
  await page.locator("[data-ip-group-name]").fill("办公终端");
  await page.locator("[data-ip-group-entries]").fill("192.168.10.0/24\n192.168.20.10-192.168.20.20");
  await page.locator("#modalOk").click();
  await page.getByText("IP 组已保存并回读确认").waitFor();
  await page.getByText("192.168.20.10-192.168.20.20").waitFor();

  await page.getByRole("button", { name: "安全控制" }).click();
  await page.getByRole("button", { name: "新增安全策略" }).click();
  await page.locator("[data-acl-name]").fill("阻断测试网段");
  await page.locator("[data-acl-source]").fill("192.168.10.0/24");
  await page.locator("[data-acl-destination]").fill("any");
  await page.locator("#modalOk").click();
  await page.getByText("安全策略已保存并回读确认").waitFor();
  await page.getByText("阻断测试网段").waitFor();

  await page.getByRole("button", { name: "系统用户管理" }).click();
  await page.getByRole("button", { name: "新增用户" }).click();
  await page.locator("[data-user-name]").fill("auditor");
  await page.locator("[data-user-password]").fill("RouteSmoke1");
  await page.locator("#modalOk").click();
  await page.getByText("系统用户已保存并回读确认").waitFor();
  await page.getByText("auditor").waitFor();

  await page.getByRole("button", { name: "配置管理" }).click();
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "导出配置" }).click();
  assert.match((await download).suggestedFilename(), /^ly-route-orchestrator-config-\d{4}-\d{2}-\d{2}\.json$/, "config export must use a product-specific filename");
  await page.getByRole("button", { name: "创建快照" }).click();
  await page.locator("[data-snapshot-name]").fill("pre-change");
  await page.locator("#modalOk").click();
  await page.getByText("配置快照已创建并回读确认").waitFor();
  await page.getByRole("button", { name: "恢复 manual-pre-change-fixture" }).click();
  await page.getByRole("button", { name: "确认恢复" }).click();
  await page.getByText("期望配置已从快照恢复，需执行运行时应用").waitFor();

  const importPackage = { package_manifest: { schema_version: 1, product: "orchestrator", secret_policy: "excluded", package_hash: "sha256:fixture" }, payload: { schema_version: 1, content_type: "local_desired_config", product: "orchestrator", device_mode: "orchestrator", resources: { object_group: [{ id: "ip-imported", name: "导入终端", kind: "ip", entries: ["10.20.0.0/16"] }], security_acl: [] }, excluded_domains: ["secrets"] } };
  await page.getByRole("button", { name: "导入配置" }).click();
  await page.locator("[data-import-file]").setInputFiles({ name: "orchestrator-config.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(importPackage)) });
  const preflight = page.waitForResponse((response) => response.request().method() === "POST" && response.url().includes("/api/v1/config/import") && response.request().postDataJSON().confirm === false);
  await page.getByRole("button", { name: "开始预检" }).click();
  await preflight;
  await page.locator("[data-import-confirm]").check();
  const imported = page.waitForResponse((response) => response.request().method() === "POST" && response.url().includes("/api/v1/config/import") && response.request().postDataJSON().confirm === true);
  await page.getByRole("button", { name: "确认导入" }).click();
  await imported;
  await page.getByText("配置已导入，需执行运行时应用").waitFor();
  await capture("retained-management");
}

async function assertEveryProductPageIsImplemented(page) {
  const pages = ["流量概况", "系统概况", "在线用户", "Top连接", "网卡设置", "编排设置", "流量编排", "流量控制", "安全控制", "IP管理", "系统用户管理", "配置管理"];
  const menuLabels = await page.locator("#sideMenu .menu-page").allTextContents();
  assert.deepEqual(menuLabels.map((label) => label.trim()), pages, "Orchestrator menu order must match the approved product boundary");
  for (const forbidden of ["接口状态", "运行态", "Top域名", "WAN群组", "路由/NAT", "端口映射", "DNS管控", "DHCP服务", "域名管理"]) {
    assert.equal(await page.getByRole("button", { name: forbidden, exact: true }).count(), 0, `${forbidden} must not be exposed by the Orchestrator menu`);
  }
  for (const title of pages) {
    await page.getByRole("button", { name: title, exact: true }).click();
    await page.getByRole("heading", { name: title, exact: true }).waitFor();
    assert.equal(await page.getByText(/已按当前产品能力注册/).count(), 0, `${title} must not render a placeholder`);
  }
}

async function runViewport(browser, baseURL, viewport) {
  console.log(`Orchestrator UI viewport: ${viewport.name}`);
  const context = await browser.newContext({ baseURL, viewport: { width: viewport.width, height: viewport.height } });
  const page = await context.newPage();
  const consoleLines = [];
  const networkLines = [];
  const pageErrors = [];
  page.on("console", (message) => consoleLines.push(`${message.type()}: ${message.text()}`));
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("response", (response) => {
    if (response.url().includes("/api/") || response.url().includes("/__fixture__/")) networkLines.push(`${response.status()} ${response.request().method()} ${new URL(response.url()).pathname}`);
  });
  const capture = (state) => page.screenshot({ path: join(evidenceDir, `${viewport.name}-${state}-${viewport.width}x${viewport.height}.png`), fullPage: true });

  await resetFixture(page);
  await page.goto("/");
  await page.getByRole("button", { name: "流量控制" }).click();
  await page.getByRole("button", { name: "新增限速规则" }).click();
  await page.getByLabel("名称").fill("办公终端上行");
  await page.getByLabel("源IP").fill("192.168.10.0/24");
  await page.getByLabel("目的IP").fill("any");
  await page.getByLabel("协议").selectOption("tcp");
  await page.getByLabel("方向").selectOption("uplink");
  await page.getByLabel("限速 Mbps").fill("20");
  const appliedRate = page.waitForResponse((response) => response.request().method() === "POST" && response.url().includes("/api/v1/runtime/apply"));
  await page.getByRole("button", { name: "确定" }).click();
  await appliedRate;
  await page.getByText("限速策略已应用并从 API 回读确认").waitFor();
  await page.getByText("办公终端上行").waitFor();
  await page.getByText("20 Mbps").waitFor();
  await capture("traffic-control");
  await assertRetainedProductManagement(page, capture);
  await assertSharedManagementFlow(page, capture);
  await resetFixture(page);
  await page.reload();
  await page.getByRole("button", { name: "网卡设置" }).click();
  await page.getByText("eth0").first().waitFor();
  await assert.doesNotReject(() => page.getByText("管理专用").waitFor());
  await page.getByText("链路断开").first().waitFor();
  await configureBond(page, "lan", ["eth1", "eth2"]);
  await configureBond(page, "wan", ["eth3", "eth4"]);
  await page.getByRole("button", { name: "保存网卡设置" }).click();
  await page.getByText("已从 API 回读确认").waitFor();
  await capture("nic-settings");

  await page.getByRole("button", { name: "编排设置" }).click();
  await page.getByRole("button", { name: "新建编排组" }).click();
  for (const owned of ["eth0", "eth1", "eth2", "eth3", "eth4"]) assert.equal(await page.locator(`[data-group-form] option[value="${owned}"]`).count(), 0, `${owned} must be absent from group membership`);
  await page.getByRole("textbox", { name: "组名称" }).fill("inline-test");
  await page.getByRole("combobox", { name: "LAN 侧端口" }).selectOption("eth5");
  await page.getByRole("combobox", { name: "WAN 侧端口" }).selectOption("eth6");
  await page.getByRole("button", { name: "确定" }).click();
  await page.getByText("inline-test").waitFor();
  await page.getByRole("button", { name: "编辑 inline-test" }).click();
  await page.getByRole("combobox", { name: "WAN 侧端口" }).selectOption("eth7");
  const editResponse = page.waitForResponse((response) => response.request().method() === "PUT" && response.url().includes("/orchestration-groups/inline-test"));
  await page.getByRole("button", { name: "确定" }).click();
  await editResponse;
  await page.getByText("已从 API 回读确认").waitFor();
  await page.locator(".orchestrator-group-table tbody td").filter({ hasText: /^eth7/ }).waitFor();
  const deleteActionBox = await page.getByRole("button", { name: "删除 inline-test" }).boundingBox();
  assert.equal(Boolean(deleteActionBox && deleteActionBox.x + deleteActionBox.width <= viewport.width), true, "group actions must be visible without horizontal scrolling");
  await capture("group-settings");
  await page.getByRole("button", { name: "删除 inline-test" }).click();
  await page.getByRole("button", { name: "确认删除" }).click();
  await page.locator(".orchestrator-group-table tbody tr").filter({ hasText: "inline-test" }).waitFor({ state: "detached" });

  const createButton = page.getByRole("button", { name: "新建编排组" });
  await createButton.focus();
  await page.keyboard.press("Enter");
  await page.getByRole("textbox", { name: "组名称" }).waitFor();
  await page.keyboard.press("Shift+Tab");
  assert.equal(await page.getByRole("button", { name: "关闭" }).evaluate((element) => element === document.activeElement), true, "focus must wrap to modal close");
  await page.keyboard.press("Escape");
  assert.equal(await createButton.evaluate((element) => element === document.activeElement), true, "modal close must restore trigger focus");

  await setMode(page, "conflict");
  await createGroup(page, "inline-conflict", "eth5", "eth6");
  await page.getByText("接口已被其他配置占用").waitFor();
  assert.equal(await page.getByText("inline-conflict").count(), 0, "conflicted group must not appear");
  await capture("conflict");

  await page.getByRole("button", { name: "网卡设置" }).click();
  await configureRole(page, "lan", "eth2");
  await setMode(page, "false-success");
  await page.getByRole("button", { name: "保存网卡设置" }).click();
  await page.getByText("保存响应与 API 回读不一致").waitFor();
  assert.equal(await page.getByText("已从 API 回读确认").count(), 0, "false success must not be shown as confirmed");
  await configureRole(page, "lan", "eth2");
  await setMode(page, "stale-readback");
  await page.getByRole("button", { name: "保存网卡设置" }).click();
  await page.getByText("当前显示的是陈旧状态").waitFor();
  await page.getByText(/^bond-lan · eth1 \+ eth2$/).waitFor();
  assert.equal(await page.getByText("单口 eth2").count(), 0, "unconfirmed NIC draft must not replace confirmed readback");

  await page.getByRole("button", { name: "编排设置" }).click();
  await setMode(page, "stale-readback");
  await createGroup(page, "inline-stale", "eth5", "eth6");
  await page.getByText("当前显示的是陈旧状态").waitFor();
  assert.equal(await page.locator(".orchestrator-group-table tbody tr").filter({ hasText: "inline-stale" }).count(), 0, "stale group create must retain the last confirmed topology");
  await capture("stale-readback");
  await page.reload();
  await page.getByRole("button", { name: "编排设置" }).click();
  await page.getByText("inline-stale").waitFor();
  await setMode(page, "api-error");
  await page.getByRole("button", { name: "删除 inline-stale" }).click();
  await page.getByRole("button", { name: "确认删除" }).click();
  await page.getByText("fixture delete failed").waitFor();

  await assertEveryProductPageIsImplemented(page);

  const requests = await (await page.request.get("/__fixture__/requests")).json();
  const forbidden = requests.items.filter((item) => /\/api\/v1\/(gateway|proxy|dns|dhcp|firmware)\//.test(item) || item.includes("/api/v1/objects/groups"));
  assert.deepEqual(forbidden, [], "Orchestrator must not request Gateway resources");
  const unexpectedConsoleErrors = consoleLines.filter((line) => line.startsWith("error:") && !/Failed to load resource: the server responded with a status of (404|409|500)/.test(line));
  assert.deepEqual(unexpectedConsoleErrors, [], "browser console must contain only expected fixture HTTP failures");
  assert.deepEqual(pageErrors, [], "browser must have no uncaught page errors");
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "page chrome must not overflow the viewport");
  await capture("api-error");
  await writeFile(join(evidenceDir, `${viewport.name}-console.log`), `${consoleLines.join("\n")}\n`);
  await writeFile(join(evidenceDir, `${viewport.name}-network.log`), `${networkLines.join("\n")}\n`);
  await context.close();
}

const temporary = await mkdtemp(join(tmpdir(), "ly-route-task18-"));
const bundleDir = join(temporary, "bundle");
await mkdir(evidenceDir, { recursive: true });
await run(join(repoRoot, "scripts/build-controller-shell.sh"), ["--product", "orchestrator", "--out", bundleDir]);
const fixture = await startOrchestratorFixture({ bundleDir, fixturePath });
const browser = await chromium.launch({ channel: "chrome", headless: true });
let passed = false;
try {
  for (const viewport of viewports) await runViewport(browser, fixture.url, viewport);
  passed = true;
} finally {
  await browser.close();
  await fixture.close();
  await rm(temporary, { recursive: true, force: true });
}
await writeFile(join(evidenceDir, "cleanup-receipt.txt"), `browser=closed\nserver=closed\nviewports=${viewports.length}\nresult=${passed ? "passed" : "failed"}\n`);
console.log(`Orchestrator UI Playwright flows passed (${viewports.length} viewports).`);
