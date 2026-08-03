(function initializeGatewayRouting() {
  function routeFromHash(pageMap, fallback) {
    const encoded = window.location.hash.slice(1);
    let route = "";
    try {
      route = decodeURIComponent(encoded);
    } catch {
      route = "";
    }
    return pageMap.has(route) ? route : fallback;
  }

  function navigate(route, replace = false) {
    const hash = `#${encodeURI(route)}`;
    if (window.location.hash === hash) return;
    window.history[replace ? "replaceState" : "pushState"](null, "", hash);
  }

  function listen(pageMap, fallback, onRoute) {
    const sync = () => onRoute(routeFromHash(pageMap, fallback));
    window.addEventListener("hashchange", sync);
    window.addEventListener("popstate", sync);
    return sync;
  }

  window.LyRouteGatewayRouting = Object.freeze({ listen, navigate, routeFromHash });
}());
