window.OFT = window.OFT || {};

OFT.settingsState = { cfg: null, autostart: false };

OFT.loadSettings = function () {
  OFT.call("GetConfig").then((cfg) => {
    OFT.settingsState.cfg = cfg;
    const active = (cfg.profiles || [])[0] || { gateway: "", port: 443 };
    document.getElementById("f-gateway").value = active.gateway || "";
    document.getElementById("f-port").value = String(active.port || 443);
    OFT.settingsState.autostart = !!cfg.autostart;
    const sw = document.getElementById("t-autostart");
    sw.classList.toggle("on", OFT.settingsState.autostart);
  });
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
    cfg.profiles[0].port = parseInt(document.getElementById("f-port").value, 10) || 443;
    cfg.autostart = OFT.settingsState.autostart;

    OFT.call("SaveConfig", cfg).then((issue) => {
      const err = document.getElementById("settings-error");
      if (issue) {
        err.textContent = issue.Message;
        err.hidden = false;
      } else {
        err.hidden = true;
        OFT.showPage("page-main");
      }
    });
  });
});
