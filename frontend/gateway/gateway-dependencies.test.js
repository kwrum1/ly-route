const test = require('node:test');
const assert = require('node:assert/strict');

require('./gateway-dependencies.js');

test('buildPlan deletes policy leaves before a referenced WAN', () => {
  const resources = {
    wanLinks: { items: [{ id: 'wan-a', name: 'WAN A' }] },
    wanGroups: { items: [{ id: 'group-a', name: 'Group A', members: ['wan-a', 'wan-b'] }] },
    proxyEgresses: { items: [{ id: 'proxy-a', name: 'Proxy A', underlay_wan_id: 'wan-a' }] },
    routePolicies: { items: [
      { id: 'route-wan', egress: 'wan-a' },
      { id: 'route-group', wan_group: 'group-a' },
      { id: 'route-proxy', egress: 'proxy-a' }
    ] },
    dnsUpstreams: { items: [{ id: 'dns-a', wan_egress_id: 'wan-a' }] },
    dnsPolicies: { items: [{ id: 'dns-policy-a', policy: { rules: [{ outcome: { upstream_id: 'dns-a' } }] } }] }
  };

  const plan = globalThis.LyRouteGatewayDependencies.buildPlan('wanLinks', 'wan-a', resources);

  assert.equal(plan.operations.at(-1).resourceId, 'wan-a');
  assert.ok(plan.dependencies.some((item) => item.resourceId === 'route-wan'));
  assert.ok(!plan.dependencies.some((item) => item.resourceId === 'route-group'));
  assert.ok(plan.dependencies.some((item) => item.resourceId === 'route-proxy'));
  assert.ok(plan.dependencies.some((item) => item.resourceId === 'dns-policy-a'));
  assert.equal(plan.operations.find((item) => item.resourceId === 'group-a').action, 'detach_member');
  assert.ok(plan.operations.findIndex((item) => item.resourceId === 'dns-policy-a') < plan.operations.findIndex((item) => item.resourceId === 'dns-a'));
});

test('buildPlan keeps unrelated resources out of the cascade', () => {
  const resources = {
    wanLinks: { items: [{ id: 'wan-a' }, { id: 'wan-b' }] },
    routePolicies: { items: [{ id: 'route-a', egress: 'wan-a' }, { id: 'route-b', egress: 'wan-b' }] },
    portMaps: { items: [{ id: 'map-b', wan_link: 'wan-b' }] }
  };

  const plan = globalThis.LyRouteGatewayDependencies.buildPlan('wanLinks', 'wan-a', resources);

  assert.deepEqual(plan.operations.map((item) => item.resourceId), ['route-a', 'wan-a']);
});

test('buildPlan deletes an empty WAN group after its policies', () => {
  const resources = {
    wanLinks: { items: [{ id: 'wan-a' }] },
    wanGroups: { items: [{ id: 'group-a', members: ['wan-a'] }] },
    routePolicies: { items: [{ id: 'route-group', wan_group: 'group-a' }] }
  };

  const plan = globalThis.LyRouteGatewayDependencies.buildPlan('wanLinks', 'wan-a', resources);

  assert.deepEqual(plan.operations.map((item) => item.resourceId), ['route-group', 'group-a', 'wan-a']);
  assert.equal(plan.operations.find((item) => item.resourceId === 'group-a').action, 'delete');
});
