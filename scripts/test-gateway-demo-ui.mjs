import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");
const baseURL = process.env.LY_ROUTE_DEMO_URL || "https://127.0.0.1:8443";
const username = process.env.LY_ROUTE_DEMO_USER || "admin";
const password = process.env.LY_ROUTE_DEMO_PASSWORD || "LyRouteDemo2026!";
const evidenceDir = process.env.LY_ROUTE_UI_EVIDENCE || join(process.cwd(), ".sisyphus/full-acceptance/evidence/g-ui-live");

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
  const pages = page.locator("[data-page]");
  const count = await pages.count();
  assert.ok(count >= 15, "the live shell must expose its operational pages");
  for (let index = 0; index < count; index += 1) {
    await pages.nth(index).click();
    await page.locator(".page-title h1").waitFor();
    assert.ok((await page.locator(".page-title h1").textContent())?.trim(), `page ${index} must have a title`);
  }
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "desktop page must not overflow horizontally");
  await page.screenshot({ path: join(evidenceDir, "desktop.png"), fullPage: true });
  await context.close();
  return count;
}

async function verifyMobile(browser, errors) {
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await login(page);
  const toggle = page.locator("#mobileMenuToggle");
  await toggle.waitFor();
  assert.equal(await toggle.isVisible(), true, "mobile navigation toggle must be visible");
  await toggle.click();
  const menu = page.locator(".paui-sidebar");
  await menu.waitFor({ state: "visible" });
  await menu.locator("[data-page]").nth(1).click();
  await page.locator(".page-title h1").waitFor();
  assert.equal(await menu.isVisible(), false, "selecting a mobile page must close the menu");
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "mobile page must not overflow horizontally");
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
console.log("Live Gateway UI browser acceptance passed.");
