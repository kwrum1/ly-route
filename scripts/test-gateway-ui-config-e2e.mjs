import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');
const baseURL = process.env.LY_ROUTE_E2E_URL || 'https://127.0.0.1:8445';
const evidenceDir = process.env.LY_ROUTE_E2E_EVIDENCE || join(process.cwd(), '.sisyphus/full-acceptance/evidence/gateway-ui-config');
const username = process.env.LY_ROUTE_DEMO_USER || 'admin';
const password = process.env.LY_ROUTE_DEMO_PASSWORD || 'LyRouteDemo2026!';

async function login(page) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await page.goto('/', { waitUntil: 'networkidle' });
    await page.locator('#username').fill(username);
    await page.locator('#password').fill(password);
    await page.locator('button[type=submit]').click();
    try {
      await page.locator('.app-shell').waitFor({ state: 'visible', timeout: 3000 });
      return;
    } catch (error) {
      await page.waitForTimeout(500);
    }
  }
  throw new Error('gateway UI did not become ready for login within 90 seconds');
}

async function openPage(page, id) {
  await page.locator(`[data-page="${id}"]`).click();
  await page.locator('.page-title h1').waitFor();
}

async function items(page, endpoint) {
  return page.evaluate(async (url) => {
    const response = await fetch(url);
    const payload = await response.json();
    if (!response.ok) throw new Error(`${response.status}: ${JSON.stringify(payload)}`);
    return payload.items || payload.data || (Array.isArray(payload) ? payload : [payload]);
  }, endpoint);
}

async function saveModal(page) {
  await page.locator('#modalOk').click();
  try {
    await page.locator('#modalBackdrop').waitFor({ state: 'hidden', timeout: 60000 });
  } catch (error) {
    const toast = await page.locator('#toast').textContent().catch(() => '');
    const title = await page.locator('#modalTitle').textContent().catch(() => '');
    throw new Error(`modal save timed out: title=${title} toast=${toast}`);
  }
}

async function selectByNeedle(page, selector, needle) {
  const select = page.locator(selector);
  const options = await select.locator('option').evaluateAll((nodes) => nodes.map((node) => ({ value: node.value, label: node.textContent || '' })));
  const option = options.find((candidate) => candidate.value === needle || candidate.label.includes(needle));
  assert.ok(option, `${selector} must include ${needle}: ${JSON.stringify(options)}`);
  await select.selectOption(option.value);
}

async function configureRole(page, interfaceName, role) {
  await openPage(page, 'monitor/interface_list');
  const row = page.locator('#workspace tr').filter({ hasText: interfaceName }).first();
  await row.waitFor({ state: 'visible', timeout: 90000 });
  await row.locator('[data-row-action=edit]').click();
  await page.locator('#modal select').selectOption(role);
  await saveModal(page);
}

async function createLAN(page) {
  await openPage(page, 'network/proxy_main');
  await page.locator('[data-tab-index="0"]').click();
  await page.locator('[data-action=add]').click();
  await page.locator('[data-lan-name]').fill('lan-e2e');
  await selectByNeedle(page, '[data-lan-interface]', 'eth1');
  await page.locator('[data-lan-address]').fill('192.168.88.1');
  await page.locator('[data-lan-prefix]').fill('24');
  await page.locator('[data-lan-mtu]').fill('1500');
  await saveModal(page);
}

async function createStaticWAN(page, name, interfaceName, address, gateway) {
  await openPage(page, 'network/proxy_main');
  await page.locator('[data-tab-index="1"]').click();
  await page.locator('[data-action=add]').click();
  await page.locator('[data-wan-name]').fill(name);
  await selectByNeedle(page, '[data-wan-interface]', interfaceName);
  await page.locator('[data-wan-type]').selectOption('static4');
  await page.locator('[data-wan-upload-mbps]').fill('100');
  await page.locator('[data-wan-download-mbps]').fill('100');
  await page.locator('[data-wan-ipv4]').fill(address);
  await page.locator('[data-wan-ipv4-prefix]').fill('24');
  await page.locator('[data-wan-ipv4-gateway]').fill(gateway);
  await saveModal(page);
}

async function createObjectGroup(page, pageID, name, members) {
  await openPage(page, pageID);
  await page.locator('[data-action=add]').click();
  const inputs = page.locator('#modal input');
  await inputs.nth(0).fill(name);
  await inputs.nth(1).fill('UI E2E');
  await saveModal(page);
  const row = page.locator('#workspace tr').filter({ hasText: name }).first();
  await row.locator('[data-row-action=edit]').click();
  await page.locator('#modal textarea').fill(members.join('\n'));
  await saveModal(page);
}

async function setAddressGroup(page, summaryName, groupID) {
  await page.locator(`[data-address-summary="${summaryName}"]`).click();
  await page.locator('[data-condition-add]').click();
  const row = page.locator('[data-address-row]').last();
  await row.locator('[data-address-type]').selectOption('ip_group');
  await row.locator('[data-address-group]').selectOption(groupID);
  await page.locator('[data-condition-confirm]').click();
}

async function createWANGroup(page, wanIDs) {
  await openPage(page, 'network/wangroup_manager');
  await page.locator('[data-action=add-line]').click();
  await page.locator('[data-wan-group-name]').fill('wan-group-e2e');
  await page.locator('[data-wan-group-mode]').selectOption('weighted');
  for (const id of wanIDs) {
    await page.locator(`[data-wan-member][value="${id}"]`).check();
    await page.locator(`[data-wan-member-weight="${id}"]`).fill('50');
  }
  await saveModal(page);
}

async function createRoutePolicy(page, groupID, wanGroupID) {
  await openPage(page, 'route/route_policy_main');
  await page.locator('[data-action=add]').click();
  await page.locator('[data-route-priority]').fill('100');
  await page.locator('[data-route-name]').fill('route-e2e');
  await setAddressGroup(page, 'src', groupID);
  await page.locator('[data-route-src-port]').fill('1024-65535');
  await page.locator('[data-route-dst-port]').fill('443');
  await page.locator('[data-route-action]').selectOption('NAT');
  await page.locator('[data-route-line]').selectOption(wanGroupID);
  await saveModal(page);
}

async function createDNSPolicy(page, ipGroupID, domainGroupID) {
  await openPage(page, 'route/dnspolicy_main');
  await page.locator('[data-action=add]').click();
  await page.locator('[data-dns-name]').fill('dns-e2e');
  await setAddressGroup(page, 'dns-src', ipGroupID);
  await page.locator('[data-dns-domain-group]').selectOption(domainGroupID);
  await page.locator('[data-dns-upstream]').fill('223.5.5.5');
  await page.locator('[data-dns-action]').selectOption({ label: '解析' });
  await saveModal(page);
}

async function createDHCP(page) {
  await openPage(page, 'network/dhcpsvr_main');
  await page.locator('[data-tab-index="0"]').click();
  await page.locator('[data-action=add]').click();
  await selectByNeedle(page, '[data-dhcp-interface]', 'lan-e2e');
  await page.locator('[data-dhcp-pool]').fill('192.168.88.100-192.168.88.199');
  await page.locator('[data-dhcp-prefix]').fill('24');
  await page.locator('[data-dhcp-gateway]').fill('192.168.88.1');
  await saveModal(page);
}

async function createTrafficControl(page, groupID) {
  await openPage(page, 'flowcontrol/flowct_main');
  await page.locator('[data-action=add]').click();
  await page.locator('[data-flow-priority]').fill('200');
  await setAddressGroup(page, 'flow-src', groupID);
  await page.locator('[data-flow-dst-port]').fill('443');
  await page.locator('[data-flow-protocol]').selectOption('tcp');
  await page.locator('[data-flow-rate]').fill('20');
  await saveModal(page);
}

async function createReadonlyUser(page) {
  await openPage(page, 'system/webuser_main');
  await page.locator('[data-action=add]').click();
  const inputs = page.locator('#modal input');
  await inputs.nth(0).fill('ui-readonly');
  await page.locator('#modal select').selectOption('readonly');
  await inputs.nth(1).fill('UiReadonly2026!');
  await inputs.nth(2).fill('UiReadonly2026!');
  await saveModal(page);
}

async function main() {
  await mkdir(evidenceDir, { recursive: true });
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const mutations = [];
  page.on('response', (response) => {
    if (response.request().method() !== 'GET' && response.url().includes('/api/v1/')) mutations.push({ status: response.status(), method: response.request().method(), url: response.url() });
  });
  try {
    await login(page);
    for (const [name, role] of [['eth1', 'lan'], ['eth2', 'wan'], ['eth3', 'wan']]) await configureRole(page, name, role);
    await createLAN(page);
    await createStaticWAN(page, 'wan-a-e2e', 'eth2', '172.52.0.2', '172.52.0.1');
    await createStaticWAN(page, 'wan-b-e2e', 'eth3', '172.53.0.2', '172.53.0.1');
    const wanLinks = await items(page, '/api/v1/gateway/wan-links');
    const wanA = wanLinks.find((item) => item.name === 'wan-a-e2e');
    const wanB = wanLinks.find((item) => item.name === 'wan-b-e2e');
    assert.ok(wanA && wanB, JSON.stringify(wanLinks));

    await createObjectGroup(page, 'object/iptab_list', 'ui-ip-group', ['192.168.88.10', '192.168.88.20-192.168.88.30', '192.168.88.0/24']);
    await createObjectGroup(page, 'object/urlgrp_list', 'ui-domain-group', ['example.com', '.example.net']);
    const groups = await items(page, '/api/v1/objects/groups');
    const ipGroup = groups.find((item) => item.name === 'ui-ip-group');
    const domainGroup = groups.find((item) => item.name === 'ui-domain-group');
    assert.deepEqual(ipGroup.members, ['192.168.88.10', '192.168.88.20-192.168.88.30', '192.168.88.0/24']);
    assert.deepEqual(domainGroup.members, ['example.com', '.example.net']);

    await createWANGroup(page, [wanA.id, wanB.id]);
    const wanGroups = await items(page, '/api/v1/gateway/wan-groups');
    const wanGroup = wanGroups.find((item) => item.name === 'wan-group-e2e');
    assert.ok(wanGroup, JSON.stringify(wanGroups));
    await createRoutePolicy(page, ipGroup.id, wanGroup.id);
    await createDNSPolicy(page, ipGroup.id, domainGroup.id);
    await createDHCP(page);
    await createTrafficControl(page, ipGroup.id);
    await createReadonlyUser(page);

    const routes = await items(page, '/api/v1/gateway/policies/routes');
    const dnsPolicies = await items(page, '/api/v1/dns/policies');
    const dhcp = await items(page, '/api/v1/dhcp/servers');
    const traffic = await items(page, '/api/v1/gateway/traffic-control');
    const users = await items(page, '/api/v1/auth/users');
    assert.ok(routes.some((item) => item.name === 'route-e2e' && item.match?.sources?.includes(ipGroup.id)), JSON.stringify(routes));
    assert.ok(dnsPolicies.some((item) => item.name === 'dns-e2e' && item.policy?.rules?.[0]?.domain_set_ids?.includes(domainGroup.id)), JSON.stringify(dnsPolicies));
    assert.ok(dhcp.some((item) => item.interface_id === 'lan-e2e' && item.subnet === '192.168.88.0/24'), JSON.stringify(dhcp));
    assert.ok(traffic.some((item) => item.rules?.[0]?.match?.sources?.includes(ipGroup.id)), JSON.stringify(traffic));
    assert.ok(users.some((item) => item.username === 'ui-readonly' && item.role === 'readonly'), JSON.stringify(users));

    const runtime = await page.evaluate(async () => (await fetch('/api/v1/runtime/status')).json());
    await page.screenshot({ path: join(evidenceDir, 'final-ui.png'), fullPage: true });
    await writeFile(join(evidenceDir, 'summary.json'), `${JSON.stringify({ result: 'passed', mutationCount: mutations.length, runtime }, null, 2)}\n`);
    await writeFile(join(evidenceDir, 'mutations.json'), `${JSON.stringify(mutations, null, 2)}\n`);
    console.log('Gateway UI configuration end-to-end acceptance passed.');
  } finally {
    await context.close();
    await browser.close();
  }
}

main().catch((error) => {
  console.error(error.stack || error);
  process.exitCode = 1;
});
