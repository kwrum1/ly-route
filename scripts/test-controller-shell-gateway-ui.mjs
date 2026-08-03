import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createRequire } from "node:module";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { startGatewayFixture } from "./fixtures/gateway-ui-fixture.mjs";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");
const run = promisify(execFile);
const repoRoot = join(import.meta.dirname, "..");
const evidenceDir = process.env.TASK10_EVIDENCE_DIR || join(repoRoot, ".omo/evidence/task-10-gateway-ui");
const viewports = [
  { name: "desktop-large", width: 1440, height: 900 },
  { name: "desktop-compact", width: 1280, height: 800 },
];

async function setMode(page, mode) {
  const response = await page.request.post("/__fixture__/mode", { data: { mode } });
  assert.equal(response.ok(), true, `fixture mode ${mode} must be accepted`);
}

async function openTrafficOverview(page) {
  await setMode(page, "normal");
  await page.goto("/#dashboard/dashboard");
  await page.getByRole("heading", { name: "流量概况" }).waitFor();
  await page.getByRole("img", { name: "逻辑出口流量" }).waitFor();
}

async function assertTrafficOverview(page) {
  assert.equal(await page.getByText("CPU使用率").count(), 0, "traffic overview must not own CPU health");
  assert.equal(await page.getByText("内存使用率").count(), 0, "traffic overview must not own memory health");
  assert.equal(await page.locator("[data-egress-legend]").count(), 4, "all production logical egresses must have stable legends");
  assert.equal(await page.locator("[data-egress-series]").count() >= 8, true, "mirrored download and upload stacks must render");
  assert.equal(await page.locator("[data-gap-count='1']").count(), 1, "missing samples must remain a visible gap");
  assert.equal(await page.getByRole("button", { name: "5分钟" }).getAttribute("aria-pressed"), "true", "five minutes must be the default window");

  const point = page.locator("[data-chart-point]").last();
  await point.hover();
  const tooltip = page.getByRole("tooltip");
  await tooltip.waitFor();
  for (const text of ["逻辑出口", "上传速率", "下载速率", "区间字节", "占总流量", "健康状态", "底层 WAN"]) {
    await assert.doesNotReject(() => tooltip.getByText(text, { exact: false }).first().waitFor(), `tooltip must include ${text}`);
  }

  const previousTotal = await page.locator("[data-visible-download]").textContent();
  await page.getByRole("button", { name: /隐藏 备用线路/ }).click();
  assert.equal(await page.getByRole("button", { name: /显示 备用线路/ }).getAttribute("aria-pressed"), "false", "legend controls must expose hidden state");
  assert.notEqual(await page.locator("[data-visible-download]").textContent(), previousTotal, "visible totals must exclude hidden egresses");

  const hourlyRequest = page.waitForResponse((response) => response.url().includes("traffic-trend?window=1h"));
  await page.getByRole("button", { name: "1小时" }).click();
  await hourlyRequest;
  assert.equal(await page.getByRole("button", { name: "1小时" }).getAttribute("aria-pressed"), "true", "window selection must settle after fresh data");
}

async function assertTruthStates(page) {
  await setMode(page, "stale");
  await page.getByRole("button", { name: "刷新流量" }).click();
  await page.getByText("数据已陈旧").waitFor();
  await page.getByText("最后成功采样").waitFor();

  await setMode(page, "offline");
  await page.getByRole("button", { name: "刷新流量" }).click();
  await page.getByText("流量采集离线，显示上次确认数据").waitFor();
  assert.equal(await page.locator("[data-egress-series]").count() > 0, true, "offline state must retain visibly marked confirmed data, respecting hidden egresses");

  await setMode(page, "unavailable");
  await page.getByRole("button", { name: "刷新流量" }).click();
  await page.getByText("尚无流量采样").waitFor();
  assert.equal(await page.locator("[data-egress-series]").count(), 0, "unavailable must not invent chart continuity");
}

async function assertSystemOverview(page) {
  await setMode(page, "normal");
  await page.goto("/#system/system_overview");
  await page.getByRole("heading", { name: "系统概况" }).waitFor();
  assert.equal(new URL(page.url()).hash, "#system/system_overview", "deep link must remain canonical");
  for (const text of ["CPU使用率", "内存使用率", "运行时长", "x86_64 appliance", "10.4.0", "系统时间", "VPP", "SmartDNS", "Kea DHCP", "Xray"]) {
    await assert.doesNotReject(() => page.getByText(text, { exact: false }).first().waitFor(), `system overview must render ${text}`);
  }
  await page.locator("[data-system-link='system/runtime_ops']").click();
  await page.getByRole("button", { name: "预览运行态" }).waitFor();
  await page.getByRole("button", { name: "应用运行态" }).waitFor();
}

async function assertModalAndReadback(page) {
  await page.getByRole("button", { name: "WAN群组" }).click();
  const trigger = page.getByRole("button", { name: "新增群组" });
  await trigger.focus();
  await page.keyboard.press("Enter");
  const name = page.locator("[data-wan-group-name]");
  await name.waitFor();
  assert.equal(await name.evaluate((element) => element === document.activeElement), true, "modal must focus its first form control");
  await page.keyboard.press("Shift+Tab");
  assert.equal(await page.getByRole("button", { name: "关闭" }).evaluate((element) => element === document.activeElement), true, "modal focus must wrap through the close control");
  await page.keyboard.press("Escape");
  assert.equal(await trigger.evaluate((element) => element === document.activeElement), true, "Escape must restore trigger focus");

  await trigger.click();
  await name.fill("虚假成功组");
  await page.locator("[data-wan-member]").first().check();
  await setMode(page, "false-success");
  await page.getByRole("button", { name: "确定" }).click();
  await page.getByText("保存响应与 API 回读不一致").waitFor();
  assert.equal(await page.locator("#modal").getAttribute("class").then((value) => value.includes("is-hidden")), false, "unconfirmed save must keep the draft modal open");
  assert.equal(await name.inputValue(), "虚假成功组", "false success must preserve the operator draft");
  assert.equal(await page.locator("table").getByText("虚假成功组", { exact: true }).count(), 0, "false success must not appear in the confirmed table");
  await page.getByRole("button", { name: "关闭" }).click();
  assert.equal(await page.locator("#modal").getAttribute("class").then((value) => value.includes("is-hidden")), true, "explicit close must remove the modal backdrop");
}

async function assertRouteAddressModes(page) {
  await page.getByRole("button", { name: "路由/NAT" }).click();
  await page.getByRole("button", { name: "新增策略" }).click();
  const form = page.locator("[data-route-policy-form]");
  await form.waitFor();
  const sourceMode = form.locator('[data-route-address-mode="src"]');
  const sourceLiteral = form.locator('[data-route-address="src"]');
  const sourceGroup = form.locator('[data-route-address-group="src"]');
  assert.equal(await sourceMode.inputValue(), "any", "new route source must default to explicit any");
  assert.equal(await sourceLiteral.isDisabled(), true, "any must not leave an ambiguous free-text source active");

  await sourceMode.selectOption("literal");
  assert.equal(await sourceLiteral.isDisabled(), false, "literal mode must enable the address input");
  await sourceLiteral.fill("192.168.1.10-192.168.1.20");
  assert.equal(await sourceLiteral.inputValue(), "192.168.1.10-192.168.1.20", "range entry must be retained verbatim for typed submission");

  await sourceMode.selectOption("ip_group");
  assert.equal(await sourceLiteral.isDisabled(), true, "IP group mode must disable the literal input");
  assert.equal(await sourceGroup.isDisabled(), false, "IP group mode must expose a stable object selector");
  assert.equal(await sourceGroup.locator("option").count() > 1, true, "fixture IP groups must be selectable");

  const groupID = await sourceGroup.locator("option").nth(1).getAttribute("value");
  assert.notEqual(groupID, null, "fixture IP group must have a stable ID");
  await sourceLiteral.focus();
  await form.locator(`[data-route-fill="${groupID}"]`).click();
  assert.equal(await sourceMode.inputValue(), "ip_group", "IP group shortcut must choose IP group mode");
  assert.equal(await sourceGroup.inputValue(), groupID, "IP group shortcut must populate the group selector, not the literal field");
  assert.equal(await sourceLiteral.isDisabled(), true, "IP group shortcut must keep the literal field disabled so its draft cannot be serialized");
  assert.equal(await sourceLiteral.inputValue(), "192.168.1.10-192.168.1.20", "mode changes must preserve an operator's literal draft without replacing it with a group ID");
  await page.keyboard.press("Escape");
}

async function assertSharedManagementEditor(page) {
  await page.goto("/#system/sys_config");
  await page.getByRole("heading", { name: "配置管理" }).waitFor();
  await page.getByRole("radio", { name: "与 LAN 共享" }).check();
  await page.locator("[data-management-cidr]").fill("10.10.10.254/24");
  await page.locator("[data-management-gateway]").fill("10.10.10.254");
  await page.locator("[data-management-confirm]").check();
  const saved = page.waitForResponse((response) => response.request().method() === "POST" && response.url().includes("/api/v1/management/network"));
  await page.getByRole("button", { name: "保存管理口" }).click();
  const response = await saved;
  const request = response.request().postDataJSON();
  assert.equal(request.mode, "shared_lan", "Gateway must submit shared_lan explicitly");
  assert.equal(request.cidr, "10.10.10.254/24", "Gateway must submit the management prefix");
  await page.getByText("应用后访问 https://10.10.10.254/").waitFor();
  assert.equal(await page.getByRole("radio", { name: "与 LAN 共享" }).isChecked(), true, "shared mode must survive API readback");
}

async function assertSmartQoSStatus(page) {
  await page.getByRole("button", { name: "流量控制" }).click();
  const status = page.getByRole("region", { name: "内置智能 QoS 状态" });
  await status.getByText("运行中", { exact: true }).waitFor();
  await status.getByText("系统内置 · 不可修改", { exact: true }).waitFor();
  await status.getByText("vpp_native", { exact: true }).waitFor();
  assert.equal(await status.locator("input, select, textarea, button").count(), 0, "built-in smart QoS must expose no mutable low-level controls");
}

async function runViewport(browser, baseURL, viewport) {
  const context = await browser.newContext({ baseURL, viewport: { width: viewport.width, height: viewport.height } });
  const page = await context.newPage();
  const consoleLines = [];
  const networkLines = [];
  const pageErrors = [];
  page.on("console", (message) => consoleLines.push(`${message.type()}: ${message.text()}`));
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("response", (response) => {
    if (response.url().includes("/api/") || response.url().includes("/__fixture__/")) networkLines.push(`${response.status()} ${response.request().method()} ${new URL(response.url()).pathname}${new URL(response.url()).search}`);
  });

  await openTrafficOverview(page);
  await assertTrafficOverview(page);
  await page.screenshot({ path: join(evidenceDir, `${viewport.name}-traffic-${viewport.width}x${viewport.height}.png`), fullPage: true });
  await assertTruthStates(page);
  await assertSystemOverview(page);
  await page.screenshot({ path: join(evidenceDir, `${viewport.name}-system-${viewport.width}x${viewport.height}.png`), fullPage: true });
  await assertModalAndReadback(page);
  await assertRouteAddressModes(page);
  await assertSharedManagementEditor(page);
  await assertSmartQoSStatus(page);

  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "desktop shell must not overflow horizontally");
  assert.deepEqual(pageErrors, [], "browser must have no uncaught page errors");
  const unexpectedErrors = consoleLines.filter((line) => line.startsWith("error:") && !line.includes("503") && !line.includes("ERR_NETWORK_CHANGED"));
  assert.deepEqual(unexpectedErrors, [], "browser console must be clean except the deliberate offline response and fixture network transition");
  await writeFile(join(evidenceDir, `${viewport.name}-console.log`), `${consoleLines.join("\n")}\n`);
  await writeFile(join(evidenceDir, `${viewport.name}-network.log`), `${networkLines.join("\n")}\n`);
  await context.close();
}

const temporary = await mkdtemp(join(tmpdir(), "ly-route-task10-"));
const bundleDir = join(temporary, "bundle");
await mkdir(evidenceDir, { recursive: true });
await run(join(repoRoot, "scripts/build-controller-shell.sh"), ["--product", "gateway", "--out", bundleDir]);
const fixture = await startGatewayFixture({ bundleDir });
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
console.log(`Gateway UI Playwright flows passed (${viewports.length} desktop viewports).`);
