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
