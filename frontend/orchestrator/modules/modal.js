(function initializeOrchestratorModal() {
  const modal = document.getElementById("modal");
  const backdrop = document.getElementById("modalBackdrop");
  const title = document.getElementById("modalTitle");
  const body = document.getElementById("modalBody");
  const closeButton = document.getElementById("modalClose");
  const cancelButton = document.getElementById("modalCancel");
  const submitButton = document.getElementById("modalOk");
  let submit = null;
  let trigger = null;
  let busy = false;

  function focusable() {
    return [...modal.querySelectorAll("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")]
      .filter((element) => !element.hidden && element.getClientRects().length > 0);
  }

  function close() {
    if (busy) return;
    modal.classList.add("is-hidden");
    backdrop.classList.add("is-hidden");
    body.innerHTML = "";
    submit = null;
    const restore = trigger;
    trigger = null;
    restore?.focus();
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

  function open(options) {
    trigger = options.trigger || document.activeElement;
    title.textContent = options.title;
    body.innerHTML = options.html;
    submitButton.textContent = options.submitLabel || "确定";
    submit = options.onSubmit;
    backdrop.classList.remove("is-hidden");
    modal.classList.remove("is-hidden");
    queueMicrotask(() => (body.querySelector("input, select, textarea") || closeButton).focus());
  }

  closeButton.addEventListener("click", close);
  cancelButton.addEventListener("click", close);
  backdrop.addEventListener("click", close);
  modal.addEventListener("keydown", onKeydown);
  submitButton.addEventListener("click", async () => {
    if (busy || !submit) return;
    const form = body.querySelector("form");
    if (form && !form.reportValidity()) return;
    busy = true;
    submitButton.disabled = true;
    cancelButton.disabled = true;
    try {
      const shouldClose = await submit(body);
      if (shouldClose !== false) {
        busy = false;
        close();
      }
    } finally {
      busy = false;
      submitButton.disabled = false;
      cancelButton.disabled = false;
    }
  });

  window.LyRouteOrchestratorModal = Object.freeze({ close, open });
}());
