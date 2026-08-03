const pageTitle = document.querySelector('[data-page-title]');
const pageSummary = document.querySelector('[data-page-summary]');
const navButtons = Array.from(document.querySelectorAll('[data-page-target]'));
const pages = Array.from(document.querySelectorAll('[data-page]'));
const pageTabs = Array.from(document.querySelectorAll('[data-persona-tab]'));
const shell = document.querySelector('[data-shell]');

const copy = {
  dashboard: {
    title: 'Operations Dashboard',
    summary: 'A topology-first landing page for Gateway and Bridge appliances with health, dataplane, and persona state visible before any table.'
  },
  network: {
    title: 'Network Fabric',
    summary: 'Persona-aware interface composition: Gateway shows WAN, LAN, service, and proxy egress; Bridge shows uplink, downlink, service, bypass, and monitor roles.'
  },
  policy: {
    title: 'Policy Studio',
    summary: 'A compact rule authoring surface with status color, priority order, and mode guard visibility built into the component language.'
  },
  system: {
    title: 'System Health',
    summary: 'Configuration lifecycle, rollback safety, datapath degradation, and modal confirmation patterns for appliance operations.'
  },
  tokens: {
    title: 'Design Tokens',
    summary: 'The project-owned visual foundation for colors, type, spacing, radius, elevation, density, charts, forms, modals, tables, cards, navigation, and topology primitives.'
  }
};

function activatePage(name) {
  pages.forEach((page) => page.classList.toggle('is-active', page.dataset.page === name));
  navButtons.forEach((button) => button.classList.toggle('is-active', button.dataset.pageTarget === name));
  pageTitle.textContent = copy[name].title;
  pageSummary.textContent = copy[name].summary;
}

function activatePersona(name) {
  shell.dataset.persona = name;
  pageTabs.forEach((button) => button.classList.toggle('is-active', button.dataset.personaTab === name));
  document.querySelectorAll('[data-persona-panel]').forEach((panel) => {
    panel.hidden = panel.dataset.personaPanel !== name;
  });
}

navButtons.forEach((button) => {
  button.addEventListener('click', () => activatePage(button.dataset.pageTarget));
});

pageTabs.forEach((button) => {
  button.addEventListener('click', () => activatePersona(button.dataset.personaTab));
});

activatePage('dashboard');
activatePersona('gateway');
