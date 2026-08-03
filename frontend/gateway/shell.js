(function initializeLyRouteShell() {
  function safeText(value) {
    return String(value).replace(/[&<>"']/g, (char) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    }[char]));
  }

  function createPageMap(sections) {
    const pageMap = new Map();
    sections.forEach((section) => section.pages.forEach((page, index) => {
      pageMap.set(page[0], {
        id: page[0],
        title: page[1],
        type: page[2],
        sectionId: section.id,
        sectionTitle: section.title,
        sectionNo: section.no,
        index,
      });
    }));
    return pageMap;
  }

  function createProductShell({ sections, initialPage, renderPage }) {
    const sideMenu = document.getElementById("sideMenu");
    const menuSearch = document.getElementById("menuSearch");
    const workspace = document.getElementById("workspace");
    const appShell = document.getElementById("appShell");
    const mobileMenuToggle = document.getElementById("mobileMenuToggle");
    const pageMap = createPageMap(sections);
    const state = { active: initialPage, query: "", collapsedSections: new Set() };

    function currentPage() {
      return pageMap.get(state.active) || pageMap.values().next().value;
    }

    function renderMenu() {
      const query = state.query.trim().toLowerCase();
      sideMenu.innerHTML = sections.map((section) => {
        const pages = section.pages
          .map((page) => pageMap.get(page[0]))
          .filter((page) => !query || `${section.title} ${page.title} ${page.id}`.toLowerCase().includes(query));
        if (!pages.length) return "";
        const collapsed = state.collapsedSections.has(section.id) && !query;
        const hasActivePage = pages.some((page) => page.id === state.active);
        return `<div class="menu-group ${collapsed ? "is-collapsed" : ""}">
          <button class="menu-head ${hasActivePage ? "is-active" : ""}" type="button" data-section="${section.id}" aria-expanded="${!collapsed}"><strong>${safeText(section.title)}</strong></button>
          <div class="menu-pages">${pages.map((page) => `<button class="menu-page ${page.id === state.active ? "is-active" : ""}" type="button" data-page="${page.id}"><span>${safeText(page.title)}</span></button>`).join("")}</div>
        </div>`;
      }).join("");
      sideMenu.querySelectorAll("[data-section]").forEach((button) => button.addEventListener("click", () => {
        const id = button.dataset.section;
        if (state.collapsedSections.has(id)) state.collapsedSections.delete(id);
        else state.collapsedSections.add(id);
        renderMenu();
      }));
      sideMenu.querySelectorAll("[data-page]").forEach((button) => button.addEventListener("click", () => {
        state.active = button.dataset.page;
        setMobileMenuOpen(false);
        render();
      }));
    }

    function setMobileMenuOpen(open) {
      const visible = Boolean(open);
      appShell?.classList.toggle("mobile-menu-open", visible);
      mobileMenuToggle?.setAttribute("aria-expanded", String(visible));
      mobileMenuToggle?.setAttribute("aria-label", visible ? "关闭菜单" : "打开菜单");
      if (mobileMenuToggle) mobileMenuToggle.title = visible ? "关闭菜单" : "打开菜单";
    }

    function render() {
      renderMenu();
      workspace.innerHTML = renderPage(currentPage(), state);
    }

    menuSearch.addEventListener("input", (event) => {
      state.query = event.target.value;
      renderMenu();
    });
    mobileMenuToggle?.addEventListener("click", (event) => {
      event.stopPropagation();
      setMobileMenuOpen(!appShell?.classList.contains("mobile-menu-open"));
    });
    document.addEventListener("click", (event) => {
      if (!appShell?.classList.contains("mobile-menu-open")) return;
      if (!sideMenu.contains(event.target) && event.target !== mobileMenuToggle) setMobileMenuOpen(false);
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") setMobileMenuOpen(false);
    });
    return { render, state };
  }

  window.LyRouteShell = Object.freeze({ createPageMap, createProductShell, safeText });
}());
