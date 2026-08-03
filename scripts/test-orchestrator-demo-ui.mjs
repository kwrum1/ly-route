import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");
const baseURL = process.env.LY_ROUTE_ORCHESTRATOR_DEMO_URL || "https://127.0.0.1:8444";
const username = process.env.LY_ROUTE_DEMO_USER || "admin";
const password = process.env.LY_ROUTE_DEMO_PASSWORD || "LyRouteDemo2026!";
const evidenceDir = process.env.LY_ROUTE_UI_EVIDENCE || join(process.cwd(), ".sisyphus/full-acceptance/evidence/o-ui-live");

async function login(page) {
  await page.goto("/", { waitUntil: "networkidle" });
  await page.locator("#username").fill(username);
  await page.locator("#password").fill(password);
  await page.locator("button[type=submit]").click();
  await page.locator(".app-shell").waitFor();
}

async function verifyDesktop(browser, errors) {
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const labels = await page.locator("#sideMenu .menu-page").allTextContents();
  const expected = ["流量概况", "系统概况", "在线用户", "Top连接", "网卡设置", "编排设置", "流量编排", "流量控制", "安全控制", "IP管理", "系统用户管理", "配置管理"];
  assert.deepEqual(labels.map((label) => label.trim()), expected, "live menu must preserve the Orchestrator product boundary");
  for (const forbidden of ["WAN群组", "路由/NAT", "端口映射", "DNS管控", "DHCP服务", "域名管理"]) {
    assert.equal(await page.getByRole("button", { name: forbidden, exact: true }).count(), 0, `${forbidden} must not be exposed`);
  }
  const pages = page.locator("#sideMenu .menu-page");
  for (let index = 0; index < await pages.count(); index += 1) {
    await pages.nth(index).click();
    const heading = page.locator(".page-title h1");
    await heading.waitFor();
    assert.ok((await heading.textContent())?.trim(), `page ${index} must render a title`);
  }
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "desktop must not overflow horizontally");
  await page.screenshot({ path: join(evidenceDir, "desktop.png"), fullPage: true });
  await context.close();
  return expected.length;
}

async function verifyMobile(browser, errors) {
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const toggle = page.locator("#mobileMenuToggle");
  await toggle.click();
  const menu = page.locator(".paui-sidebar");
  await menu.waitFor({ state: "visible" });
  await menu.locator(".menu-page").nth(1).click();
  await page.locator(".page-title h1").waitFor();
  assert.equal(await menu.isVisible(), false, "choosing a mobile page must close the menu");
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "mobile must not overflow horizontally");
  await page.screenshot({ path: join(evidenceDir, "mobile.png"), fullPage: true });
  await context.close();
}

await mkdir(evidenceDir, { recursive: true });
const browser = await chromium.launch({ channel: "chrome", headless: true });
const errors = [];
try {
  const pageCount = await verifyDesktop(browser, errors);
  await verifyMobile(browser, errors);
  assert.deepEqual(errors, [], "live browser must have no uncaught page errors");
  await writeFile(join(evidenceDir, "summary.json"), `${JSON.stringify({ baseURL, pageCount, mobileNavigation: true, result: "passed" }, null, 2)}\n`);
} finally {
  await browser.close();
}
console.log("Live Orchestrator UI browser acceptance passed.");
