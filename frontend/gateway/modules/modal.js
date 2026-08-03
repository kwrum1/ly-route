(function initializeGatewayModal() {
  function create(elements, options) {
    let trigger = null;

    function focusable() {
      return [...elements.modal.querySelectorAll("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")]
        .filter((element) => !element.hidden && element.getClientRects().length > 0);
    }

    function close(force = false) {
      if (options.isBusy() && !force) return;
      elements.backdrop.classList.add("is-hidden");
      elements.modal.classList.add("is-hidden");
      elements.modal.classList.remove("modal-route-policy");
      options.onClose();
      const restore = trigger;
      trigger = null;
      restore?.focus();
    }

    function open({ title, html, variant = "", onReady }) {
      trigger = document.activeElement;
      elements.modal.classList.toggle("modal-route-policy", variant === "route-policy");
      elements.title.textContent = title;
      elements.body.innerHTML = html;
      elements.backdrop.classList.remove("is-hidden");
      elements.modal.classList.remove("is-hidden");
      onReady();
      queueMicrotask(() => (elements.body.querySelector("input, select, textarea") || elements.closeButton).focus());
    }

    function onKeydown(event) {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
        return;
      }
      if (event.key !== "Tab") return;
      const controls = focusable();
      const first = controls[0];
      const last = controls[controls.length - 1];
      if (!first || !last) return;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    elements.closeButton.addEventListener("click", () => close());
    elements.cancelButton.addEventListener("click", () => close());
    elements.backdrop.addEventListener("click", () => close());
    elements.modal.addEventListener("keydown", onKeydown);
    return Object.freeze({ close, open });
  }

  window.LyRouteGatewayModal = Object.freeze({ create });
}());
