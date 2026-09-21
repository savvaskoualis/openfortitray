window.OFT = window.OFT || {};

OFT.call = function (method, ...args) {
  const ns = window.go && window.go.main && window.go.main.Bridge;
  if (!ns || typeof ns[method] !== "function") {
    console.error("OFT.call: no bound method", method);
    return Promise.reject(new Error("not bound: " + method));
  }
  return ns[method](...args);
};

OFT.showPage = function (id) {
  document.querySelectorAll(".page").forEach((el) => (el.hidden = el.id !== id));
};

// OFT.modal replaces window.confirm/alert: Wails' darwin WKUIDelegate never
// implements the JS alert/confirm/prompt panel methods, so those calls are
// silent no-ops in this webview -- no dialog ever appears, and confirm()
// returns false immediately, so the code path it guards just never runs.
// Resolves to true on OK, false on Cancel/dismiss.
OFT.modal = function (message, { showCancel = false } = {}) {
  return new Promise((resolve) => {
    const overlay = document.getElementById("modal-overlay");
    document.getElementById("modal-message").textContent = message;
    document.getElementById("modal-input").hidden = true;
    const okBtn = document.getElementById("modal-ok");
    const cancelBtn = document.getElementById("modal-cancel");
    cancelBtn.hidden = !showCancel;

    const done = (result) => {
      overlay.hidden = true;
      okBtn.onclick = null;
      cancelBtn.onclick = null;
      resolve(result);
    };
    okBtn.onclick = () => done(true);
    cancelBtn.onclick = () => done(false);
    overlay.hidden = false;
  });
};
OFT.confirm = (message) => OFT.modal(message, { showCancel: true });
OFT.alertBox = (message) => OFT.modal(message, { showCancel: false });

// OFT.promptText is OFT.modal's text-input sibling, replacing
// window.prompt() for the same reason (silent no-op in this webview).
// Resolves to the trimmed entered text, or null on Cancel/empty.
OFT.promptText = function (message, { placeholder = "" } = {}) {
  return new Promise((resolve) => {
    const overlay = document.getElementById("modal-overlay");
    document.getElementById("modal-message").textContent = message;
    const input = document.getElementById("modal-input");
    input.value = "";
    input.placeholder = placeholder;
    input.hidden = false;
    const okBtn = document.getElementById("modal-ok");
    const cancelBtn = document.getElementById("modal-cancel");
    cancelBtn.hidden = false;

    const done = (result) => {
      overlay.hidden = true;
      input.hidden = true;
      okBtn.onclick = null;
      cancelBtn.onclick = null;
      input.onkeydown = null;
      resolve(result);
    };
    okBtn.onclick = () => done(input.value.trim() || null);
    cancelBtn.onclick = () => done(null);
    input.onkeydown = (e) => {
      if (e.key === "Enter") done(input.value.trim() || null);
    };
    overlay.hidden = false;
    input.focus();
  });
};

window.addEventListener("DOMContentLoaded", () => {
  document.getElementById("btn-open-settings").addEventListener("click", () => OFT.showPage("page-settings"));
  document.getElementById("btn-back").addEventListener("click", () => OFT.showPage("page-main"));

  OFT.call("IsDarkMode").then((dark) => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
  });

  window.addEventListener("blur", () => OFT.call("HideWindow"));

  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn("nav:settings", () => {
      OFT.showPage("page-settings");
      // A connect-issue error (or anything else navigating here directly,
      // bypassing the gear-icon button's own click handler) still needs the
      // form populated — otherwise Save operates on OFT.settingsState.cfg
      // while it's still null.
      if (typeof OFT.loadSettings === "function") {
        OFT.loadSettings();
      }
    });
    window.runtime.EventsOn("nav:status", () => OFT.showPage("page-main"));
  }
});
