window.OFT = window.OFT || {};

OFT.settingsState = {
  cfg: null,
  activeIdx: 0,
  autostart: false,
  dtls: false,
  rememberSession: false,
  certMode: "warn",
};

OFT.activeProfile = function () {
  return OFT.settingsState.cfg.profiles[OFT.settingsState.activeIdx];
};

// resolveActiveIdx finds cfg.activeProfile by name (Config.Active's own
// lookup, mirrored here) rather than assuming index 0, so opening Settings
// shows whichever profile the tray is actually configured to dial.
OFT.resolveActiveIdx = function (cfg) {
  const idx = (cfg.profiles || []).findIndex((p) => p.name === cfg.activeProfile);
  return idx >= 0 ? idx : 0;
};

OFT.closeProfileMenu = function () {
  document.getElementById("profile-menu").hidden = true;
  document.getElementById("profile-pill").setAttribute("aria-expanded", "false");
};

OFT.renderProfileMenu = function () {
  const cfg = OFT.settingsState.cfg;
  const menu = document.getElementById("profile-menu");
  menu.innerHTML = "";

  cfg.profiles.forEach((p, idx) => {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "profile-item";
    const label = document.createElement("span");
    label.textContent = p.name || "(unnamed)";
    item.appendChild(label);
    if (idx === OFT.settingsState.activeIdx) {
      const check = document.createElement("span");
      check.className = "check";
      check.textContent = "✓";
      item.appendChild(check);
    }
    item.addEventListener("click", () => {
      OFT.settingsState.activeIdx = idx;
      cfg.activeProfile = p.name;
      OFT.renderProfileMenu();
      OFT.renderActiveProfile();
      OFT.closeProfileMenu();
    });
    menu.appendChild(item);
  });

  menu.appendChild(document.createElement("hr"));

  const add = document.createElement("button");
  add.type = "button";
  add.className = "profile-item add";
  add.textContent = "+ Add profile";
  add.addEventListener("click", async () => {
    OFT.closeProfileMenu();
    const name = await OFT.promptText("New profile name", { placeholder: "e.g. Home lab" });
    if (!name) return;
    const p = await OFT.call("NewProfileTemplate", name);
    cfg.profiles.push(p);
    OFT.settingsState.activeIdx = cfg.profiles.length - 1;
    cfg.activeProfile = name;
    OFT.renderProfileMenu();
    OFT.renderActiveProfile();
  });
  menu.appendChild(add);
};

OFT.setCertMode = function (mode) {
  OFT.settingsState.certMode = mode;
  document.querySelectorAll("#cert-mode-group .radio-chip").forEach((chip) => {
    chip.classList.toggle("active", chip.dataset.mode === mode);
  });
  document.getElementById("cert-pin-field").hidden = mode !== "pin";
};

OFT.setAdvancedOpen = function (open) {
  document.getElementById("disclosure-advanced").classList.toggle("open", open);
  document.getElementById("disclosure-advanced").setAttribute("aria-expanded", String(open));
  document.getElementById("advanced-body").hidden = !open;
};

// renderActiveProfile paints every field from the profile at activeIdx.
// Called on load and every time the profile switcher picks a different one
// — switching never round-trips to the backend, it just repaints from the
// working copy already held in OFT.settingsState.cfg.
OFT.renderActiveProfile = function () {
  const p = OFT.activeProfile();
  document.getElementById("profile-pill-label").textContent = p.name || "Settings";
  document.getElementById("f-gateway").value = p.gateway || "";
  // 10443 is FortiGate's actual default SSL-VPN port (not the generic
  // HTTPS 443) — must match internal/config.Config's real default.
  document.getElementById("f-port").value = String(p.port || 10443);

  OFT.settingsState.dtls = !!p.dtls;
  document.getElementById("t-dtls").classList.toggle("on", OFT.settingsState.dtls);
  OFT.settingsState.rememberSession = !!p.remember_session;
  document.getElementById("t-remember-session").classList.toggle("on", OFT.settingsState.rememberSession);

  OFT.setCertMode((p.server_cert && p.server_cert.mode) || "warn");
  document.getElementById("f-cert-pin").value = (p.server_cert && p.server_cert.pin) || "";

  document.getElementById("f-split-dns").value = (p.split_dns || []).join("\n");
};

OFT.loadSettings = function () {
  OFT.call("GetConfig").then((cfg) => {
    if (!cfg.profiles || cfg.profiles.length === 0) {
      cfg.profiles = [{ name: "Default", port: 10443, dtls: true, remember_session: true, server_cert: { mode: "warn" } }];
      cfg.activeProfile = "Default";
    }
    OFT.settingsState.cfg = cfg;
    OFT.settingsState.activeIdx = OFT.resolveActiveIdx(cfg);
    OFT.settingsState.autostart = !!cfg.autostart;
    document.getElementById("t-autostart").classList.toggle("on", OFT.settingsState.autostart);

    OFT.renderProfileMenu();
    OFT.renderActiveProfile();
    // Collapse advanced on every fresh load, so reopening Settings never
    // shows it stuck open from a prior visit.
    OFT.setAdvancedOpen(false);
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
  document.getElementById("t-dtls").addEventListener("click", () => {
    OFT.settingsState.dtls = !OFT.settingsState.dtls;
    document.getElementById("t-dtls").classList.toggle("on", OFT.settingsState.dtls);
  });
  document.getElementById("t-remember-session").addEventListener("click", () => {
    OFT.settingsState.rememberSession = !OFT.settingsState.rememberSession;
    document.getElementById("t-remember-session").classList.toggle("on", OFT.settingsState.rememberSession);
  });

  document.getElementById("disclosure-advanced").addEventListener("click", () => {
    OFT.setAdvancedOpen(!document.getElementById("disclosure-advanced").classList.contains("open"));
  });
  document.querySelectorAll("#cert-mode-group .radio-chip").forEach((chip) => {
    chip.addEventListener("click", () => OFT.setCertMode(chip.dataset.mode));
  });

  document.getElementById("profile-pill").addEventListener("click", (e) => {
    e.stopPropagation();
    const menu = document.getElementById("profile-menu");
    const willOpen = menu.hidden;
    menu.hidden = !willOpen;
    document.getElementById("profile-pill").setAttribute("aria-expanded", String(willOpen));
  });
  // Click-outside closes the profile menu, matching any other native
  // dropdown; the pill's own handler above already stops its click from
  // reaching this listener, so opening and immediately closing can't race.
  document.addEventListener("click", (e) => {
    if (!document.getElementById("profile-switcher").contains(e.target)) OFT.closeProfileMenu();
  });

  document.getElementById("btn-cancel").addEventListener("click", () => OFT.showPage("page-main"));

  document.getElementById("btn-save").addEventListener("click", () => {
    const cfg = OFT.settingsState.cfg;
    const p = OFT.activeProfile();
    p.gateway = document.getElementById("f-gateway").value;
    p.port = parseInt(document.getElementById("f-port").value, 10) || 10443;
    p.dtls = OFT.settingsState.dtls;
    p.remember_session = OFT.settingsState.rememberSession;
    p.server_cert = p.server_cert || {};
    p.server_cert.mode = OFT.settingsState.certMode;
    p.server_cert.pin = OFT.settingsState.certMode === "pin" ? document.getElementById("f-cert-pin").value.trim() : "";
    p.split_dns = document
      .getElementById("f-split-dns")
      .value.split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    cfg.autostart = OFT.settingsState.autostart;
    cfg.activeProfile = p.name;

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
