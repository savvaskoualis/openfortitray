window.OFT = window.OFT || {};

OFT.renderView = function (v) {
  const icons = { disconnected: "icon-disconnected", connecting: "icon-connecting", connected: "icon-connected", error: "icon-error" };
  const kindNames = { 0: "disconnected", 1: "connecting", 2: "connected", 3: "error" }; // uistate.Kind: Idle, Busy, OK, Bad
  const kind = kindNames[v.Kind] || "disconnected";

  Object.values(icons).forEach((id) => (document.getElementById(id).hidden = true));
  document.getElementById(icons[kind]).hidden = false;

  const ring = document.getElementById("status-ring");
  ring.style.borderColor = { disconnected: "var(--secondary)", connecting: "var(--accent)", connected: "var(--green)", error: "var(--red)" }[kind];
  ring.style.color = ring.style.borderColor;

  document.getElementById("state-label").textContent = v.Title || "Disconnected";
  document.getElementById("sub-label").textContent = v.Detail || "";

  const btn = document.getElementById("btn-primary");
  if (kind === "connected") {
    btn.textContent = "Disconnect";
    btn.classList.add("secondary");
  } else if (kind === "connecting") {
    btn.textContent = "Cancel";
    btn.classList.add("secondary");
  } else {
    btn.textContent = kind === "error" ? "Retry" : "Connect";
    btn.classList.remove("secondary");
  }

  const details = document.getElementById("details-card");
  details.hidden = kind !== "connected";
  if (kind === "connected") {
    document.getElementById("d-ip").textContent = v.AssignedIP || "";
    OFT.call("GatewayLabel").then((label) => {
      document.getElementById("d-gateway").textContent = label || "";
    });
    OFT.call("DTLSLabel").then((label) => {
      document.getElementById("d-protocol").textContent = label || "";
    });
  }
};

OFT.renderActivity = function (events) {
  const list = document.getElementById("activity-list");
  list.innerHTML = "";
  events.forEach((ev) => {
    const row = document.createElement("div");
    row.className = "activity-row";
    row.innerHTML = `<div class="t"></div><div class="m"></div>`;
    row.querySelector(".t").textContent = ev.time;
    row.querySelector(".m").textContent = ev.msg;
    list.appendChild(row);
  });
};

// refreshActivity pulls the current activity log from the backend and
// renders it. Called once on load and again on every "tunnel:event" push,
// since a new event means pump() just appended a new entry server-side.
OFT.refreshActivity = function () {
  OFT.call("RecentActivity").then((entries) => {
    OFT.renderActivity((entries || []).map((e) => ({ time: new Date(e.At).toLocaleTimeString(), msg: e.Text })));
  });
};

window.addEventListener("DOMContentLoaded", () => {
  OFT.call("CurrentView").then(OFT.renderView);
  OFT.refreshActivity();
  // main.version is stamped from the release tag (e.g. "v1.2.3", already
  // carrying its own "v"), or "dev" for an unstamped local build — render it
  // as-is rather than prepending another "v".
  OFT.call("Version").then((v) => {
    document.getElementById("version-label").textContent = v;
  });

  document.getElementById("btn-primary").addEventListener("click", () => {
    const label = document.getElementById("btn-primary").textContent;
    if (label === "Connect" || label === "Retry") OFT.call("Connect");
    else OFT.call("Disconnect");
  });

  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn("tunnel:event", (v) => {
      OFT.renderView(v);
      OFT.refreshActivity();
    });

    window.runtime.EventsOn("update:check-result", (r) => {
      OFT.alertBox(r.heading + "\n\n" + r.body);
    });

    window.runtime.EventsOn("update:offer", (tag) => {
      document.getElementById("update-badge").hidden = false;
      OFT.confirm("OpenFortiTray " + tag + " is available. Download it now? (The VPN stays connected during download.)").then((ok) => {
        if (ok) OFT.call("DownloadUpdate");
      });
    });
    window.runtime.EventsOn("update:ready", (tag) => {
      document.getElementById("update-badge").hidden = false;
      OFT.confirm("OpenFortiTray " + tag + " is ready to install. Restart now? (The app will close and reopen automatically.)").then((ok) => {
        if (ok) OFT.call("RestartAndInstall");
      });
    });
    window.runtime.EventsOn("update:failed", (err) => {
      OFT.alertBox("The update could not be downloaded. Nothing has changed.\n\n" + err);
    });
  }
});
