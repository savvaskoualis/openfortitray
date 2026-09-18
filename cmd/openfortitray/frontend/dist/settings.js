window.OFT = window.OFT || {};

OFT.settingsState = { cfg: null, autostart: false };

OFT.loadSettings = function () {
  OFT.call("GetConfig").then((cfg) => {
    OFT.settingsState.cfg = cfg;
    // 10443 is FortiGate's actual default SSL-VPN port (not the generic
    // HTTPS 443) — must match internal/config.Config's real default.
    const active = (cfg.profiles || [])[0] || { gateway: "", port: 10443 };
    document.getElementById("f-gateway").value = active.gateway || "";
    document.getElementById("f-port").value = String(active.port || 10443);
    OFT.settingsState.autostart = !!cfg.autostart;
    const sw = document.getElementById("t-autostart");
    sw.classList.toggle("on", OFT.settingsState.autostart);
  });
};

// showSettingsError renders message into the settings page's error banner.
// Shared by SaveConfig's validation-failure path and the "settings:issue"
// event (Connect refused because the config isn't ready to dial — see
// main.go's onConnectIssue), so both surfaces show an error identically.
OFT.showSettingsError = function (message) {
  const err = document.getElementById("settings-error");
  err.textContent = message;
  err.hidden = false;
};

window.addEventListener("DOMContentLoaded", () => {
  document.getElementById("btn-open-settings").addEventListener("click", OFT.loadSettings);

  document.getElementById("tab-connection").addEventListener("click", () => {
    document.getElementById("tab-connection").classList.add("active");
    document.getElementById("tab-advanced").classList.remove("active");
    document.getElementById("pane-connection").hidden = false;
    document.getElementById("pane-advanced").hidden = true;
  });
  document.getElementById("tab-advanced").addEventListener("click", () => {
    document.getElementById("tab-advanced").classList.add("active");
    document.getElementById("tab-connection").classList.remove("active");
    document.getElementById("pane-advanced").hidden = false;
    document.getElementById("pane-connection").hidden = true;
  });

  document.getElementById("t-autostart").addEventListener("click", () => {
    OFT.settingsState.autostart = !OFT.settingsState.autostart;
    document.getElementById("t-autostart").classList.toggle("on", OFT.settingsState.autostart);
  });

  document.getElementById("btn-cancel").addEventListener("click", () => OFT.showPage("page-main"));

  document.getElementById("btn-save").addEventListener("click", () => {
    const cfg = OFT.settingsState.cfg;
    if (!cfg.profiles || cfg.profiles.length === 0) cfg.profiles = [{}];
    cfg.profiles[0].gateway = document.getElementById("f-gateway").value;
    cfg.profiles[0].port = parseInt(document.getElementById("f-port").value, 10) || 10443;
    cfg.autostart = OFT.settingsState.autostart;

    OFT.call("SaveConfig", cfg).then((issue) => {
      if (issue) {
        OFT.showSettingsError(issue.Message);
      } else {
        document.getElementById("settings-error").hidden = true;
        OFT.showPage("page-main");
      }
    });
  });

  if (window.runtime && window.runtime.EventsOn) {
    // Connect was refused because the active config isn't ready to dial
    // (main.go's onConnectIssue) — ShowSettings already navigated here via
    // "nav:settings" (app.js); this renders the reason into the same error
    // banner SaveConfig's validation-failure path uses.
    window.runtime.EventsOn("settings:issue", (message) => {
      OFT.showSettingsError(message);
    });
  }
});
