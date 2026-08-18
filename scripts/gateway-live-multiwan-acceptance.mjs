import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const baseURL = process.env.LY_ROUTE_E2E_URL || 'https://10.1.18.125';
const username = process.env.LY_ROUTE_E2E_USER || 'admin';
const password = process.env.LY_ROUTE_E2E_PASSWORD || 'password';
const secondInterface = process.env.LY_ROUTE_SECOND_WAN_INTERFACE || 'eth3';
const pppoeUsername = process.env.LY_ROUTE_PPPOE_USER || 'lyroute-test';
const pppoePassword = process.env.LY_ROUTE_PPPOE_PASSWORD || 'PppoeTest2026!';
const evidenceDir = process.env.LY_ROUTE_E2E_EVIDENCE || join(process.cwd(), '.acceptance/evidence/gateway-live-multiwan');

async function login(page) {
  await page.goto('/', { waitUntil: 'networkidle' });
  await page.locator('#username').fill(username);
  await page.locator('#password').fill(password);
  await page.locator('button[type=submit]').click();
  await page.locator('.app-shell').waitFor({ state: 'visible', timeout: 15000 });
}

async function openPage(page, id) {
  await page.locator(`[data-page="${id}"]`).click();
  await page.locator('.page-title h1').waitFor({ state: 'visible' });
}

async function saveModal(page) {
  await page.locator('#modalOk').click();
  try {
    await page.locator('#modalBackdrop').waitFor({ state: 'hidden', timeout: 60000 });
  } catch (error) {
    const toast = await page.locator('#toast').textContent().catch(() => '');
    const modal = await page.locator('#modalBody').textContent().catch(() => '');
    throw new Error(`modal save failed: toast=${toast} modal=${modal}`);
  }
}

async function api(page, path, options) {
  return page.evaluate(async ({ path, options }) => {
    const response = await fetch(path, options);
    const text = await response.text();
    let body;
    try { body = JSON.parse(text); } catch { body = text; }
    if (!response.ok) throw new Error(`${options?.method || 'GET'} ${path} ${response.status}: ${text}`);
    return body;
  }, { path, options });
}

function items(envelope) {
  return envelope?.items || envelope?.data || envelope || [];
}

async function selectNeedle(page, selector, needle) {
  const select = page.locator(selector);
  const options = await select.locator('option').evaluateAll(nodes => nodes.map(node => ({ value: node.value, label: node.textContent || '' })));
  const option = options.find(item => item.value === needle || item.label.includes(needle));
  assert.ok(option, `${selector} does not contain ${needle}: ${JSON.stringify(options)}`);
  await select.selectOption(option.value);
}

async function configureWANRole(page) {
  const interfaces = items(await api(page, '/api/v1/interfaces'));
  const target = interfaces.find(item => item.id === secondInterface || item.name === secondInterface || item.system_name === secondInterface);
  assert.ok(target, `second WAN interface not found: ${secondInterface}`);
  if (target.gateway_role === 'wan') return target;

  await openPage(page, 'monitor/interface_list');
  const row = page.locator('#workspace tr').filter({ hasText: secondInterface }).first();
  await row.locator('[data-row-action=edit]').click();
  await page.locator('#modal select').selectOption('wan');
  await saveModal(page);
  const refreshed = items(await api(page, '/api/v1/interfaces'));
  return refreshed.find(item => item.id === target.id || item.system_name === target.system_name) || target;
}

async function createSecondPPPoE(page, targetInterface) {
  const aliases = new Set([secondInterface, targetInterface?.id, targetInterface?.name, targetInterface?.system_name].filter(Boolean).map(String));
  const links = items(await api(page, '/api/v1/gateway/wan-links'));
  await openPage(page, 'network/proxy_main');
  await page.locator('[data-tab-index="1"]').click();
  const existing = links.find(item => aliases.has(String(item.interface_id || '')) || aliases.has(String(item.system_name || '')));
  if (existing) {
    if (existing.username === pppoeUsername && process.env.LY_ROUTE_FORCE_WAN_SAVE !== '1') return existing;
    const editButtons = page.locator('#workspace [data-row-action=edit]');
    const candidates = await editButtons.evaluateAll(nodes => nodes.map(node => ({
      text: node.closest('tr')?.textContent || node.parentElement?.textContent || '',
      index: Number(node.dataset.row || node.dataset.index || -1),
    })));
    const targetIndex = candidates.findIndex(candidate => candidate.text.includes(existing.name || existing.id));
    assert.ok(targetIndex >= 0, `WAN row not found for ${existing.id}: ${JSON.stringify(candidates)}`);
    await editButtons.nth(targetIndex).click();
    await selectNeedle(page, '[data-wan-interface]', targetInterface.system_name || secondInterface);
    await page.locator('[data-wan-username]').fill(pppoeUsername);
    await page.locator('[data-wan-password]').fill(pppoePassword);
    await saveModal(page);
    const updated = items(await api(page, '/api/v1/gateway/wan-links'));
    const edited = updated.find(item => item.id === existing.id);
    assert.equal(edited?.username, pppoeUsername, JSON.stringify(updated));
    return edited;
  }

  await page.locator('[data-action=add]').click();
  await page.locator('[data-wan-name]').fill('PPPoE Backup');
  await selectNeedle(page, '[data-wan-interface]', secondInterface);
  await page.locator('[data-wan-type]').selectOption('pppoe');
  await page.locator('[data-wan-username]').fill(pppoeUsername);
  await page.locator('[data-wan-password]').fill(pppoePassword);
  await page.locator('[data-wan-upload-mbps]').fill('100');
  await page.locator('[data-wan-download-mbps]').fill('100');
  await saveModal(page);

  const updated = items(await api(page, '/api/v1/gateway/wan-links'));
  const created = updated.find(item => aliases.has(String(item.interface_id || '')) || aliases.has(String(item.system_name || '')));
  assert.ok(created, JSON.stringify(updated));
  return created;
}

async function waitForTwoSessions(page) {
  const deadline = Date.now() + 60000;
  while (Date.now() < deadline) {
    const status = items(await api(page, '/api/v1/gateway/pppoe/status'));
    const connected = status.filter(item => item.state === 'connected' && item.route_ready);
    if (connected.length >= 2) {
      assert.ok(connected.every(item => item.ac_mac), `connected PPPoE sessions do not expose upstream AC MACs: ${JSON.stringify(connected)}`);
      const acMacs = connected.map(item => item.ac_mac.toLowerCase());
      assert.equal(new Set(acMacs).size, acMacs.length, `PPPoE sessions share an upstream AC MAC: ${JSON.stringify(connected)}`);
      return connected;
    }
    await page.waitForTimeout(1000);
  }
  throw new Error(`two PPPoE sessions did not connect: ${JSON.stringify(await api(page, '/api/v1/gateway/pppoe/status'))}`);
}

async function main() {
  await mkdir(evidenceDir, { recursive: true });
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const mutations = [];
  page.on('response', async response => {
    if (response.request().method() === 'GET' || !response.url().includes('/api/v1/')) return;
    mutations.push({ method: response.request().method(), status: response.status(), url: response.url(), body: response.request().postData() || '' });
  });

  try {
    await login(page);
    const targetInterface = await configureWANRole(page);
    const secondWAN = await createSecondPPPoE(page, targetInterface);
    const sessions = await waitForTwoSessions(page);
    const interfaces = await api(page, '/api/v1/interfaces');
    const links = await api(page, '/api/v1/gateway/wan-links');
    const runtime = await api(page, '/api/v1/runtime/status');
    assert.equal(runtime.last_apply?.status, 'committed', JSON.stringify(runtime));
    await openPage(page, 'network/proxy_main');
    await page.locator('[data-tab-index="1"]').click();
    await page.screenshot({ path: join(evidenceDir, 'second-pppoe-wan.png'), fullPage: false });
    await writeFile(join(evidenceDir, 'result.json'), `${JSON.stringify({ result: 'passed', secondWAN, sessions, interfaces, links, runtime, mutations }, null, 2)}\n`);
    console.log(JSON.stringify({ result: 'passed', second_wan: secondWAN.id, sessions: sessions.map(item => ({ peer_id: item.peer_id, interface: item.interface, assigned_ipv4: item.assigned_ipv4, ac_mac: item.ac_mac })) }));
  } finally {
    await context.close();
    await browser.close();
  }
}

main().catch(error => {
  console.error(error.stack || error);
  process.exitCode = 1;
});
