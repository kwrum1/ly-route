import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const baseURL = process.env.LY_ROUTE_E2E_URL || 'https://10.1.18.125';
const username = process.env.LY_ROUTE_E2E_USER || 'admin';
const password = process.env.LY_ROUTE_E2E_PASSWORD || 'password';
const evidenceDir = process.env.LY_ROUTE_E2E_EVIDENCE || join(process.cwd(), '.sisyphus/full-acceptance/evidence/gateway-live-ui');
const acceptanceMarker = 'LY_ROUTE_GATEWAY_UI_ACCEPTANCE';
const acceptanceNamePrefix = '[E2E Gateway]';
const preserveAcceptanceResources = process.env.LY_ROUTE_E2E_PRESERVE === '1';

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

async function api(page, url, options) {
  return page.evaluate(async ({ url, options }) => {
    const response = await fetch(url, options);
    const body = await response.text();
    let payload;
    try { payload = JSON.parse(body); } catch { payload = body; }
    if (!response.ok) throw new Error(`${options?.method || 'GET'} ${url} ${response.status}: ${body}`);
    return payload;
  }, { url, options });
}

function items(value) {
  return value?.items || value?.data || value || [];
}

function referencesAnyID(resource, ids) {
  const serialized = JSON.stringify(resource);
  return ids.some(id => serialized.includes(`"${id}"`));
}

function isAcceptanceNamed(resource) {
  return String(resource?.name || '').startsWith(acceptanceNamePrefix);
}

async function deleteAcceptanceResources(page, endpoint, resources, predicate, deleted) {
  for (const resource of resources.filter(predicate)) {
    if (!resource?.id) continue;
    await api(page, `${endpoint}/${encodeURIComponent(resource.id)}`, { method: 'DELETE' });
    deleted.push(`${endpoint}/${resource.id}`);
  }
}

async function cleanupAcceptanceArtifacts(page) {
  const endpoints = {
    groups: '/api/v1/objects/groups',
    routes: '/api/v1/gateway/policies/routes',
    dns: '/api/v1/dns/policies',
    upstreams: '/api/v1/dns/upstreams',
    traffic: '/api/v1/gateway/traffic-control',
    portMaps: '/api/v1/gateway/nat/port-maps'
  };
  const resources = Object.fromEntries(await Promise.all(Object.entries(endpoints).map(async ([key, endpoint]) => [key, items(await api(page, endpoint))])));
  const acceptanceGroups = resources.groups.filter(resource =>
    resource?.description === acceptanceMarker ||
    resource?.description === '真实 UI 全功能验收' ||
    isAcceptanceNamed(resource)
  );
  const groupIDs = acceptanceGroups.map(resource => String(resource.id));
  const acceptanceDNS = resources.dns.filter(resource =>
    isAcceptanceNamed(resource) ||
    referencesAnyID(resource, groupIDs)
  );
  const dnsIDs = acceptanceDNS.map(resource => String(resource.id));
  const deleted = [];

  // Delete consumers first so object-group reference protection remains useful.
  await deleteAcceptanceResources(page, endpoints.dns, resources.dns, resource => dnsIDs.includes(String(resource.id)), deleted);
  await deleteAcceptanceResources(page, endpoints.routes, resources.routes, resource => isAcceptanceNamed(resource) || referencesAnyID(resource, groupIDs), deleted);
  await deleteAcceptanceResources(page, endpoints.traffic, resources.traffic, resource => isAcceptanceNamed(resource) || referencesAnyID(resource, groupIDs), deleted);
  await deleteAcceptanceResources(page, endpoints.portMaps, resources.portMaps, resource =>
    isAcceptanceNamed(resource) ||
    (/^msu[0-9a-z]+-/i.test(String(resource?.id || '')) && /^端口映射msu[0-9a-z]+$/i.test(String(resource?.name || '')))
  , deleted);
  const generatedUpstreamIDs = new Set(dnsIDs.map(id => `dns-policy-${id}-upstream`));
  await deleteAcceptanceResources(page, endpoints.upstreams, resources.upstreams, resource => generatedUpstreamIDs.has(String(resource.id)), deleted);
  await deleteAcceptanceResources(page, endpoints.groups, resources.groups, resource => groupIDs.includes(String(resource.id)), deleted);

  let runtimeApply = null;
  if (deleted.length > 0) {
    runtimeApply = await api(page, '/api/v1/runtime/apply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}'
    });
    assert.equal(runtimeApply.status, 'committed', JSON.stringify(runtimeApply));
  }
  return { deleted, runtimeApply };
}

async function saveModal(page) {
  await page.locator('#modalOk').click();
  await page.locator('#modalBackdrop').waitFor({ state: 'hidden', timeout: 60000 });
}

async function selectNeedle(page, selector, needle) {
  const select = page.locator(selector);
  const options = await select.locator('option').evaluateAll(nodes => nodes.map(node => ({ value: node.value, label: node.textContent || '' })));
  const option = options.find(item => item.value === needle || item.label.includes(needle));
  assert.ok(option, `${selector} does not contain ${needle}: ${JSON.stringify(options)}`);
  await select.selectOption(option.value);
  return option.value;
}

async function createObjectGroup(page, pageID, name, members) {
  await openPage(page, pageID);
  const groupTab = page.locator('[data-tab-index="0"]');
  if (await groupTab.count()) await groupTab.click();
  console.log('OBJECT_PAGE', pageID, await page.locator('.page-title h1').textContent(), await page.locator('[data-tab-index]').allTextContents(), await page.locator('[data-action=add]').allTextContents());
  await page.locator('[data-action=add]').click();
  const inputs = page.locator('#modal input');
  await inputs.nth(0).fill(name);
  await inputs.nth(1).fill(acceptanceMarker);
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

async function main() {
  await mkdir(evidenceDir, { recursive: true });
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const mutations = [];
  page.on('response', response => {
    if (response.request().method() !== 'GET' && response.url().includes('/api/v1/')) {
      mutations.push({ status: response.status(), method: response.request().method(), url: response.url() });
      console.log('MUTATION', response.status(), response.request().method(), response.url(), response.request().postData() || '');
    }
  });

  let loggedIn = false;
  let primaryError = null;
  try {
    await login(page);
    loggedIn = true;
    const preflightCleanup = await cleanupAcceptanceArtifacts(page);
    await writeFile(join(evidenceDir, 'preflight-cleanup.json'), `${JSON.stringify(preflightCleanup, null, 2)}\n`);
    const interfaces = await api(page, '/api/v1/interfaces');
    const interfaceItems = interfaces.items || interfaces;
    const lan = interfaceItems.find(item => item.gateway_role === 'lan');
    const wan = interfaceItems.find(item => item.gateway_role === 'wan');
    assert.ok(lan && wan, JSON.stringify(interfaceItems));
    const wanLinksEnvelope = await api(page, '/api/v1/gateway/wan-links');
    const wanLinks = wanLinksEnvelope.items || wanLinksEnvelope;
    const wanLink = wanLinks.find(item => item.interface_id === wan.system_name || item.system_name === wan.system_name || item.type === 'pppoe');
    assert.ok(wanLink, JSON.stringify(wanLinks));
    const lanResourceID = lan.system_name || lan.id;

    const suffix = Date.now().toString(36);
    const priorityBase = 400 + (Date.now() % 400);
    const externalPort = 20000 + (Date.now() % 20000);
    const ipGroupName = `${acceptanceNamePrefix} IP ${suffix}`;
    const domainGroupName = `${acceptanceNamePrefix} Domain ${suffix}`;
    await createObjectGroup(page, 'object/iptab_list', ipGroupName, ['192.168.50.101', '192.168.50.120-192.168.50.125', '192.168.50.0/24']);
    await createObjectGroup(page, 'object/urlgrp_list', domainGroupName, ['example.com', '.example.net']);
    const groups = await api(page, '/api/v1/objects/groups');
    const groupItems = groups.items || groups;
    const ipGroup = groupItems.find(item => item.name === ipGroupName);
    const domainGroup = groupItems.find(item => item.name === domainGroupName);
    assert.ok(ipGroup && domainGroup, JSON.stringify(groupItems));

    await openPage(page, 'route/route_policy_main');
    await page.locator('[data-action=add]').click();
    await page.locator('[data-route-priority]').fill(String(priorityBase));
    await page.locator('[data-route-name]').fill(`${acceptanceNamePrefix} Route ${suffix}`);
    await setAddressGroup(page, 'src', ipGroup.id);
    await page.locator('[data-route-dst-port]').fill('443');
    await page.locator('[data-route-action]').selectOption('NAT');
    await selectNeedle(page, '[data-route-line]', wanLink.id);
    await saveModal(page);

    await openPage(page, 'route/dnspolicy_main');
    await page.locator('[data-action=add]').click();
    await page.locator('[data-dns-priority]').fill(String(priorityBase + 1));
    await page.locator('[data-dns-name]').fill(`${acceptanceNamePrefix} DNS ${suffix}`);
    await setAddressGroup(page, 'dns-src', ipGroup.id);
    await page.locator('[data-dns-domain-group]').selectOption(domainGroup.id);
    await selectNeedle(page, '[data-dns-line]', wanLink.id);
    await page.locator('[data-dns-bootstrap-profile]').selectOption('domestic');
    await page.locator('[data-dns-upstream]').fill('223.5.5.5');
    await page.locator('[data-dns-action]').selectOption({ label: '解析' });
    await saveModal(page);

    await openPage(page, 'network/dhcpsvr_main');
    await page.locator('[data-tab-index="0"]').click();
    await page.locator('[data-action=add]').click();
    await selectNeedle(page, '[data-dhcp-interface]', lanResourceID);
    await page.locator('[data-dhcp-pool]').fill('192.168.50.150-192.168.50.180');
    await page.locator('[data-dhcp-prefix]').fill('24');
    await page.locator('[data-dhcp-gateway]').fill('192.168.50.1');
    await saveModal(page);

    await openPage(page, 'flowcontrol/flowct_main');
    await page.locator('[data-action=add]').click();
    await page.locator('[data-flow-priority]').fill(String(priorityBase + 2));
    await setAddressGroup(page, 'flow-src', ipGroup.id);
    await page.locator('[data-flow-dst-port]').fill('443');
    await page.locator('[data-flow-protocol]').selectOption('tcp');
    await page.locator('[data-flow-rate]').fill('20');
    await saveModal(page);

    const existingPortMaps = items(await api(page, '/api/v1/gateway/nat/port-maps'));
    const usedInternalEndpoints = new Set(existingPortMaps.map(item => [
      String(item.protocol || '').toLowerCase(),
      String(item.internal_host || ''),
      String(item.internal_port || ''),
    ].join(':')));
    const internalHost = Array.from({ length: 55 }, (_, index) => 200 + index)
      .map(octet => `192.168.50.${octet}`)
      .find(host => !usedInternalEndpoints.has(`tcp:${host}:8080`));
    assert.ok(internalHost, 'no unused LAN endpoint remains for the port-map acceptance fixture');

    await openPage(page, 'route/portmap_list');
    await page.locator('[data-action=add]').click();
    await page.locator('[data-portmap-name]').fill(`${acceptanceNamePrefix} Port Map ${suffix}`);
    await selectNeedle(page, '[data-portmap-wan]', wanLink.id);
    await page.locator('[data-portmap-external-port]').fill(String(externalPort));
    await page.locator('[data-portmap-internal-host]').fill(internalHost);
    await page.locator('[data-portmap-internal-port]').fill('8080');
    await saveModal(page);

    const readbacks = {};
    for (const [key, endpoint] of Object.entries({
      groups: '/api/v1/objects/groups',
      routes: '/api/v1/gateway/policies/routes',
      dns: '/api/v1/dns/policies',
      dhcp: '/api/v1/dhcp/servers',
      traffic: '/api/v1/gateway/traffic-control',
      portMaps: '/api/v1/gateway/nat/port-maps',
      runtime: '/api/v1/runtime/status'
    })) readbacks[key] = await api(page, endpoint);

    assert.ok(items(readbacks.groups).some(item => item.id === ipGroup.id && item.entries.includes('192.168.50.120-192.168.50.125')));
    assert.ok(items(readbacks.groups).some(item => item.id === domainGroup.id && item.entries.includes('.example.net')));
    assert.ok(items(readbacks.routes).some(item => item.priority === priorityBase && item.match?.sources?.includes(ipGroup.id)));
    assert.ok(items(readbacks.dns).some(item => item.priority === priorityBase + 1 && item.policy?.rules?.[0]?.domain_set_ids?.includes(domainGroup.id)));
    assert.ok(items(readbacks.dhcp).some(item => item.interface_id === lanResourceID && item.pools?.includes('192.168.50.150-192.168.50.180')));
    assert.ok(items(readbacks.traffic).some(item => item.rules?.some(rule => rule.match?.sources?.includes(ipGroup.id))));
    assert.ok(items(readbacks.portMaps).some(item => item.external_port === externalPort && item.internal_host === internalHost));
    assert.equal(readbacks.runtime.last_apply?.status, 'committed');

    await page.screenshot({ path: join(evidenceDir, 'gateway-live-ui.png'), fullPage: false, timeout: 15000 });
    await writeFile(join(evidenceDir, 'mutations.json'), `${JSON.stringify(mutations, null, 2)}\n`);
    await writeFile(join(evidenceDir, 'readbacks.json'), `${JSON.stringify(readbacks, null, 2)}\n`);
    await writeFile(join(evidenceDir, 'summary.json'), `${JSON.stringify({ result: 'passed', ipGroup: ipGroup.id, domainGroup: domainGroup.id, priorityBase, externalPort, internalHost, mutationCount: mutations.length }, null, 2)}\n`);
    console.log('Gateway live UI configuration acceptance passed.');
  } catch (error) {
    primaryError = error;
  } finally {
    try {
      if (loggedIn && !preserveAcceptanceResources) {
        const finalCleanup = await cleanupAcceptanceArtifacts(page);
        await writeFile(join(evidenceDir, 'final-cleanup.json'), `${JSON.stringify(finalCleanup, null, 2)}\n`);
      }
    } catch (cleanupError) {
      if (primaryError) console.error('Acceptance cleanup also failed:', cleanupError.stack || cleanupError);
      else primaryError = cleanupError;
    } finally {
      await context.close();
      await browser.close();
    }
  }
  if (primaryError) throw primaryError;
}

main().catch(error => {
  console.error(error.stack || error);
  process.exitCode = 1;
});
