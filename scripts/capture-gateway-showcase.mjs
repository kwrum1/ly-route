import { createRequire } from "node:module";
import { mkdir } from "node:fs/promises";

const { chromium } = createRequire(import.meta.url)("playwright");

const out = "docs/screenshots";
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true, executablePath: chromium.executablePath() });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.addInitScript(() => {
  let logged = false;
  const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  window.fetch = async (input) => {
    const path = new URL(typeof input === "string" ? input : input.url, location.href).pathname;
    if (path.endsWith("/auth/session")) return logged ? json({ authenticated: true, username: "admin", password_change_required: false }) : json({}, 401);
    if (path.endsWith("/auth/login")) { logged = true; return json({ authenticated: true, session: { password_change_required: false } }); }
    if (path.endsWith("/auth/logout")) { logged = false; return json({}); }
    if (path.includes("/telemetry/traffic-trend")) return json({ items: Array.from({ length: 24 }, (_, i) => ({ timestamp: `2026-08-04T${String(i).padStart(2, "0")}:00:00Z`, rx_bps: 12000000 + Math.sin(i) * 3500000, tx_bps: 8000000 + Math.cos(i) * 2200000 })) });
    if (path.includes("/interfaces")) return json({ items: [{ id: "eth0", name: "eth0", status: "up", role: "LAN", rx_bps: 18200000, tx_bps: 9200000 }, { id: "eth1", name: "eth1", status: "up", role: "WAN", rx_bps: 9200000, tx_bps: 18200000 }] });
    return json({ items: [], data: {}, capabilities: [] });
  };
});
const page = await context.newPage();
await page.goto("http://127.0.0.1:8765/index.html?mock=1", { waitUntil: "networkidle" });
await page.screenshot({ path: `${out}/gateway-login.png`, fullPage: true });
await page.locator("#username").fill("admin");
await page.locator("#password").fill("password");
await page.locator("button[type=submit]").click();
await page.locator(".app-shell").waitFor({ state: "visible" });
await page.waitForTimeout(500);
await page.screenshot({ path: `${out}/gateway-system-overview.png`, fullPage: true });
const targets = await page.locator("[data-page]").evaluateAll((links) => links.map((link) => link.dataset.page).filter(Boolean));
console.log(`Showcase pages: ${targets.join(", ")}`);
for (const target of targets.filter((target) => ["dashboard/dashboard", "network/interface_list", "route/route_policy_main"].includes(target))) {
  const link = page.locator(`[data-page="${target}"]`);
  if (await link.count()) { await link.first().click(); await page.waitForTimeout(300); await page.screenshot({ path: `${out}/gateway-${target.replace("/", "-")}.png`, fullPage: true }); }
}
await browser.close();
console.log("Gateway showcase screenshots captured.");
