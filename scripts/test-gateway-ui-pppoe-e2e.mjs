import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");
const baseURL = process.env.LY_ROUTE_E2E_URL || "https://127.0.0.1:8445";
const evidenceDir = process.env.LY_ROUTE_E2E_EVIDENCE || join(process.cwd(), ".sisyphus/full-acceptance/evidence/gateway-ui-pppoe");
const user = "admin";
const password = "LyRouteDemo2026!";
const gatewayContainer = "ly-route-gateway-e2e-vpp";
const pppoeContainer = "ly-route-gateway-e2e-pppoe";
const lanContainer = "ly-route-gateway-e2e-lan-client";

function dockerExec(...args) {
  return execFileSync("docker", ["exec", ...args], { encoding: "utf8" }).trim();
}

async function login(page) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await page.goto("/", { waitUntil: "networkidle" });
    await page.locator("#username").fill(user);
    await page.locator("#password").fill(password);
    await page.locator("button[type=submit]").click();
    try {
      await page.locator(".app-shell").waitFor({ state: "visible", timeout: 3000 });
      return;
    } catch (error) {
      await page.waitForTimeout(500);
    }
  }
  throw new Error("gateway UI did not become ready for login within 90 seconds");
}

async function openPage(page, id) {
  await page.locator(`[data-page="${id}"]`).click();
  await page.locator(".page-title h1").waitFor();
}

async function saveModal(page) {
  await page.locator("#modalOk").click();
  try {
    await page.locator("#modalBackdrop").waitFor({ state: "hidden", timeout: 60000 });
  } catch (error) {
    const toast = await page.locator("#toast").textContent().catch(() => "");
    const body = await page.locator("#modalBody").textContent().catch(() => "");
    const interfaces = await page.evaluate(async () => (await fetch('/api/v1/interfaces')).json()).catch(() => null);
    throw new Error(`modal save did not complete: toast=${toast} body=${body} interfaces=${JSON.stringify(interfaces)}`);
  }
}

async function selectInterface(page, selector, needle) {
  const select = page.locator(selector);
  const options = await select.locator("option").evaluateAll((items) => items.map((item) => ({ value: item.value, label: item.textContent || "" })));
  const option = options.find((item) => item.label.includes(needle) || item.value === needle);
  assert.ok(option, `interface option ${needle} must be available: ${JSON.stringify(options)}`);
  await select.selectOption(option.value);
}

async function configureRole(page, interfaceName, role) {
  await openPage(page, "monitor/interface_list");
  const target = await page.evaluate(async (id) => {
    const response = await fetch('/api/v1/interfaces');
    const payload = await response.json();
    const candidates = payload.items || payload.data || payload;
    const item = candidates.find((candidate) => candidate.system_name === id && candidate.vpp_interface)
      || candidates.find((candidate) => candidate.id === id);
    return { id: item?.id || id, displayName: item?.display_name || item?.name || id };
  }, interfaceName);
  const row = page.locator("#workspace tr").filter({ hasText: target.displayName }).first();
  await row.waitFor({ state: "visible", timeout: 90000 });
  await row.locator("[data-row-action=edit]").click();
  await page.locator("#modal select").selectOption(role);
  await saveModal(page);
  return target.id;
}

async function main() {
  await mkdir(evidenceDir, { recursive: true });
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const requests = [];
  page.on("request", (request) => {
    if (request.method() !== "GET" && request.url().includes("/api/v1/")) {
      requests.push({ method: request.method(), url: request.url(), body: request.postData() || "" });
      console.log(`mutation request ${request.method()} ${request.url()} ${request.postData() || ""}`);
    }
  });
  page.on("response", async (response) => {
    if (response.request().method() === "GET" || !response.url().includes("/api/v1/")) return;
    console.log(`mutation ${response.status()} ${response.request().method()} ${response.url()}`);
  });
  try {
    await login(page);
    const readiness = await page.evaluate(async () => {
      const response = await fetch('/api/v1/interfaces');
      return { status: response.status, body: await response.json() };
    });
    console.log(`interface readiness ${JSON.stringify(readiness)}`);
    const lanInterfaceID = await configureRole(page, "eth1", "lan");
    const wanInterfaceID = await configureRole(page, "eth2", "wan");

    await openPage(page, "network/proxy_main");
    await page.locator('[data-tab-index="1"]').click();
    await page.locator("[data-action=add]").click();
    await page.locator("[data-wan-name]").fill("pppoe-e2e");
    await selectInterface(page, "[data-wan-interface]", wanInterfaceID);
    await page.locator("[data-wan-type]").selectOption("pppoe");
    await page.locator("[data-wan-username]").fill("e2e-user");
    await page.locator("[data-wan-password]").fill("e2e-password");
    await page.locator("[data-wan-upload-mbps]").fill("100");
    await page.locator("[data-wan-download-mbps]").fill("100");
    await saveModal(page);

    const links = await page.evaluate(async () => (await fetch("/api/v1/gateway/wan-links")).json());
    const items = links.items || links.data || links;
    const pppoe = items.find((item) => item.id === "wan-eth2-pppoe" || item.name === "pppoe-e2e");
    assert.ok(pppoe, `PPPoE WAN must be persisted: ${JSON.stringify(links)}`);
    assert.equal(pppoe.type, "pppoe");
    const interfacePayload = await page.evaluate(async () => (await fetch('/api/v1/interfaces')).json());
    const interfaces = interfacePayload.items || interfacePayload.data || interfacePayload;
    const selectedWAN = interfaces.find((item) => item.id === wanInterfaceID);
    const wanAliases = [selectedWAN?.id, selectedWAN?.name, selectedWAN?.system_name, selectedWAN?.vpp_interface]
      .filter(Boolean)
      .map(String);
    assert.equal(wanAliases.includes(String(pppoe.interface_id)), true, `WAN readback must identify the selected interface: ${JSON.stringify({ wanInterfaceID, wanAliases, pppoe })}`);
    assert.equal(pppoe.username, "e2e-user");
    assert.equal(JSON.stringify(pppoe).includes("e2e-password"), false, "WAN readback must not expose the password");

    for (let attempt = 0; attempt < 45; attempt += 1) {
      const initial = dockerExec(gatewayContainer, "sh", "-lc", "vppctl show pppoe session 2>/dev/null || true");
      if (initial.includes("client-ip 10.67.0.10")) break;
      if (attempt === 44) assert.fail("initial PPPoE apply must settle before enabling LAN IPv6");
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }

    await openPage(page, "network/proxy_main");
    await page.locator('[data-tab-index="0"]').click();
    await page.locator("[data-action=add]").click();
    await page.locator("[data-lan-name]").fill("lan-e2e");
    await selectInterface(page, "[data-lan-interface]", lanInterfaceID);
    await page.locator("[data-lan-address]").fill("192.168.88.1");
    await page.locator("[data-lan-prefix]").fill("24");
    await page.locator("[data-lan-mtu]").fill("1500");
    await page.locator("[data-lan-ipv6-enabled]").check();
    await selectInterface(page, "[data-lan-prefix-wan]", pppoe.id);
    await saveModal(page);

    let pppInterface = "";
    for (let attempt = 0; attempt < 30; attempt += 1) {
      const sessions = dockerExec(gatewayContainer, "sh", "-lc", "vppctl show pppoe session 2>/dev/null || true");
      if (/client-ip 10\.67\.0\.1[0-3]/.test(sessions)) {
        pppInterface = "pppoe_session0";
        break;
      }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }
    assert.ok(pppInterface, "UI-applied PPPoE WAN must create a native VPP PPPoE session");
    const serverInterfaces = dockerExec(pppoeContainer, "sh", "-lc", "ip -o link show | awk -F': ' '$2 ~ /^ppp/ {print $2}'");
    assert.ok(serverInterfaces, "PPPoE server container must observe the live session");

    const status = await page.evaluate(async () => (await fetch("/api/v1/gateway/pppoe/status")).json());
    assert.equal((status.items || status.data || [status]).some((item) => item.state === "connected"), true, JSON.stringify(status));

    const vppSession = dockerExec(gatewayContainer, "vppctl", "show", "pppoe", "session");
    const vppAddress = dockerExec(gatewayContainer, "vppctl", "show", "interface", "address", pppInterface);
    assert.match(vppSession, /client-ip 10\.67\.0\.1[0-3]/);
    assert.match(vppAddress, /10\.67\.0\.1[0-3]\/32/);
    // VPP 25.10's TAP virtio path can retain non-fragmented LAN packets in
    // ip4-sv-reassembly-feature. Production NIC paths keep this feature; the
    // container fixture disables it so the functional LAN/NAT assertions test
    // the actual forwarding path rather than a TAP-only queue issue.
    dockerExec(gatewayContainer, "vppctl", "set", "interface", "feature", "lyroute-eth1", "ip4-sv-reassembly-feature", "arc", "ip4-unicast", "disable");
    const lanGatewayMAC = dockerExec(gatewayContainer, "sh", "-lc", "vppctl show hardware-interfaces lyroute-eth1 | awk '/Ethernet address/{print $3}'");
    assert.match(lanGatewayMAC, /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/i);
    const lanClientMAC = dockerExec(lanContainer, "sh", "-lc", "ip link show eth0 | awk '$1==\"link/ether\"{print $2}'");
    assert.match(lanClientMAC, /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/i);
    dockerExec(gatewayContainer, "vppctl", "set", "ip", "neighbor", "lyroute-eth1", "192.168.88.10", lanClientMAC);
    dockerExec(lanContainer, "sh", "-lc", `ip addr flush dev eth0; ip addr add 192.168.88.10/24 dev eth0; ip route replace default via 192.168.88.1; ip neigh replace 192.168.88.1 lladdr ${lanGatewayMAC} nud permanent dev eth0; ping -c 3 -W 2 10.67.0.1`);
    const natInterfaces = dockerExec(gatewayContainer, "vppctl", "show", "nat44", "interfaces");
      assert.match(natInterfaces.replace(/\s+/g, " "), /pppoe_session0 out/i);

    let delegated = "";
    for (let attempt = 0; attempt < 30; attempt += 1) {
      const lanIPv6 = dockerExec(gatewayContainer, "sh", "-lc", "vppctl show interface address lyroute-eth1 2>/dev/null || true");
      if (/2001:db8:100:[0-9a-f]*::1\/64/i.test(lanIPv6)) { delegated = lanIPv6; break; }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }
    assert.ok(delegated, "VPP DHCPv6-PD must install a delegated /64 on the selected LAN");
    const ra = dockerExec(gatewayContainer, "sh", "-lc", "vppctl show ip6-nd ra config lyroute-eth1 2>/dev/null || vppctl show ip6 neighbors lyroute-eth1 2>/dev/null || true");
    dockerExec(lanContainer, "sh", "-lc", "sysctl -w net.ipv6.conf.eth0.accept_ra=2 >/dev/null; ip -6 addr flush dev eth0 scope global; ip -6 route flush default; timeout 30 sh -c 'until ip -6 route show default | grep -q default; do sleep 1; done'; ip -6 addr show dev eth0 scope global; ping -6 -c 3 -W 2 2001:db8:ffff::1");
    await page.screenshot({ path: join(evidenceDir, "pppoe-wan-ui.png"), fullPage: true });
    await writeFile(join(evidenceDir, "requests.json"), `${JSON.stringify(requests, null, 2)}\n`);
    await writeFile(join(evidenceDir, "vpp-session.txt"), `${vppSession}\n`);
    await writeFile(join(evidenceDir, "vpp-address.txt"), `${vppAddress}\n`);
    await writeFile(join(evidenceDir, "nat-interfaces.txt"), `${natInterfaces}\n`);
    await writeFile(join(evidenceDir, "vpp-ipv6-lan.txt"), `${delegated}\n`);
    await writeFile(join(evidenceDir, "vpp-ipv6-ra.txt"), `${ra}\n`);
    await writeFile(join(evidenceDir, "summary.json"), `${JSON.stringify({ result: "passed", pppInterface, serverInterfaces, lanToPPPoE: "passed", pppoeIPv6PDRA: "passed", requestCount: requests.length }, null, 2)}\n`);
    console.log("Gateway UI PPPoE end-to-end acceptance passed.");
  } finally {
    await context.close();
    await browser.close();
  }
}

main().catch((error) => {
  console.error(error.stack || error);
  process.exitCode = 1;
});
