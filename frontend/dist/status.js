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

  document.getElementById("state-label").textContent = v.Title || "Not Connected";
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

window.addEventListener("DOMContentLoaded", () => {
  OFT.call("CurrentView").then(OFT.renderView);

  document.getElementById("btn-primary").addEventListener("click", () => {
    const label = document.getElementById("btn-primary").textContent;
    if (label === "Connect" || label === "Retry") OFT.call("Connect");
    else OFT.call("Disconnect");
  });

  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn("tunnel:event", OFT.renderView);
  }
});
