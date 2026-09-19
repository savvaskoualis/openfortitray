# Qt6 → Wails UI Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace OpenFortiTray's Qt6/miqt UI with a Wails-based UI (Go + native OS webview, plain HTML/CSS/JS frontend) matching the approved Tailscale-style mock, reusing all existing backend logic unchanged.

**Architecture:** A new exported `Bridge` struct in `cmd/openfortitray/wailsapp.go` wraps the existing unexported `app` struct and exposes the methods Wails binds to JS. `a.pump()`'s three `Apply()` calls become one `runtime.EventsEmit`. `internal/tray` is rewritten on `github.com/energye/systray` (keeping its pure icon/badge PNG logic). `internal/shell`, `internal/status`, `internal/uitheme`, `internal/uidispatch`, and all `glass_*`/`qtapp.go` files are deleted. `internal/settings/logic.go` is untouched; only its Qt render layer (`settings.go`) goes.

**Tech Stack:** Go 1.22, `github.com/wailsapp/wails/v2`, `github.com/energye/systray`, plain HTML/CSS/JS (no frontend build step, no npm).

**Spec:** `docs/superpowers/specs/2026-09-18-wails-ui-migration-design.md`

## Global Constraints

- No frontend build tooling (no npm, no bundler) — `frontend/dist/*` is committed source, embedded directly via `//go:embed all:frontend/dist`.
- The `app` struct in `main.go` stays unexported; JS-bound methods live on a new exported `Bridge` struct that holds `a *app` and delegates — never rename `app` to `App`.
- `internal/settings/logic.go` and `internal/settings/logic_test.go` are never touched — that logic is framework-agnostic and already correct.
- `internal/config`, `internal/tunnel`, `internal/update` (non-UI), `internal/credstore`, `internal/auth`, `internal/dns`, `internal/autostart`, `internal/xopen`, `internal/uistate` are never touched.
- Every deleted Qt-specific `_test.go` file is deleted in the same task/commit that deletes the code it tests — never leave a red build.
- Window is frameless, fixed 380×600, matching the approved mock exactly (colors: light `#F7F8FA`/`#FFFFFF`/`#E6E8EB`/`#101828`/`#667085`/`#2F6FED`, dark `#1C1D1F`/`#232427`/`#333438`/`#F2F2F3`/`#9AA0A6`).
- Live per-keystroke field validation in Settings is a non-goal for this pass — Save calls `Bridge.SaveConfig`, which returns a `*settings.Issue` (nil on success) that the frontend renders as an inline error banner. This matches how `settings.FirstConnectIssue` already gates Connect today.

---

### Task 1: Dependencies + frontend asset scaffold

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `wails.json`
- Create: `frontend/dist/index.html`
- Create: `frontend/dist/main.css`
- Create: `frontend/dist/app.js`
- Create: `frontend/dist/status.js`
- Create: `frontend/dist/settings.js`

**Interfaces:**
- Produces: the CSS custom properties (`--bg`, `--card-bg`, `--border`, `--text`, `--secondary`, `--accent`) every later frontend task styles against; the `OFT.bind(name, ...args)` JS helper every later frontend task calls to reach Go.

- [ ] **Step 1: Add Go dependencies**

```bash
go get github.com/wailsapp/wails/v2@v2.9.2
go get github.com/energye/systray@v1.0.14
go mod tidy
```

- [ ] **Step 2: Create `wails.json`**

```json
{
  "$schema": "https://wails.io/schemas/config.v2.json",
  "name": "openfortitray",
  "outputfilename": "openfortitray",
  "frontend:install": "",
  "frontend:build": "",
  "frontend:dir": "frontend",
  "wailsjsdir": "frontend",
  "author": {
    "name": "OpenFortiTray"
  }
}
```

- [ ] **Step 3: Write `frontend/dist/index.html`**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>OpenFortiTray</title>
<link rel="stylesheet" href="./main.css">
</head>
<body>

<div id="page-main" class="page">
  <div class="titlebar" style="--wails-draggable: drag;">
    <div class="titlebar-left">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2 4 5v6c0 5 3.5 9 8 11 4.5-2 8-6 8-11V5z"/></svg>
      <span>OpenFortiTray</span>
    </div>
    <button id="btn-open-settings" class="icon-btn" aria-label="Settings" style="--wails-draggable: no-drag;">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.6 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
    </button>
  </div>

  <div class="body">
    <div class="hero">
      <div id="status-ring" class="status-ring">
        <svg id="icon-disconnected" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2 4 5v6c0 5 3.5 9 8 11 4.5-2 8-6 8-11V5z"/></svg>
        <svg id="icon-connecting" class="spin" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" hidden><path d="M21 12a9 9 0 1 1-3-6.7"/></svg>
        <svg id="icon-connected" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" hidden><path d="M20 6 9 17l-5-5"/></svg>
        <svg id="icon-error" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" hidden><path d="M12 9v4m0 4h.01M10.3 3.9 2.5 17a2 2 0 0 0 1.7 3h15.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/></svg>
      </div>
      <div id="state-label" class="state-label">Not Connected</div>
      <div id="sub-label" class="sub-label"></div>
    </div>

    <button id="btn-primary" class="btn-primary">Connect</button>

    <div id="details-card" class="card details-card" hidden>
      <div class="row"><span class="k">Gateway</span><span id="d-gateway" class="v"></span></div>
      <div class="row"><span class="k">Protocol</span><span id="d-protocol" class="v"></span></div>
      <div class="row"><span class="k">IP Address</span><span id="d-ip" class="v"></span></div>
      <div class="row"><span class="k">Connected since</span><span id="d-since" class="v"></span></div>
    </div>

    <div class="activity">
      <div class="section-label">Recent activity</div>
      <div id="activity-list" class="card activity-list"></div>
    </div>
  </div>

  <div class="footer">
    <span id="version-label">v0.3.0</span>
    <span id="update-badge" class="update-badge" hidden>
      <span class="dot"></span>Update available
    </span>
  </div>
</div>

<div id="page-settings" class="page" hidden>
  <div class="titlebar" style="--wails-draggable: drag;">
    <button id="btn-back" class="icon-btn" aria-label="Back" style="--wails-draggable: no-drag;">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 18l-6-6 6-6"/></svg>
    </button>
    <span>Settings</span>
  </div>

  <div class="tabs">
    <button id="tab-connection" class="tab active">Connection</button>
    <button id="tab-advanced" class="tab">Advanced</button>
  </div>

  <div class="body">
    <div id="pane-connection" class="pane">
      <label class="field">Gateway host
        <input id="f-gateway" type="text">
      </label>
      <label class="field">Port
        <input id="f-port" type="text" style="width: 90px;">
      </label>
    </div>

    <div id="pane-advanced" class="pane" hidden>
      <div class="toggle-row">
        <div class="toggle-text"><span>Auto-connect on launch</span><span class="hint">Connect automatically when the app starts</span></div>
        <button id="t-autostart" class="switch" role="switch"><span class="knob"></span></button>
      </div>
    </div>

    <div id="settings-error" class="settings-error" hidden></div>
  </div>

  <div class="settings-footer">
    <button id="btn-cancel" class="btn-secondary">Cancel</button>
    <button id="btn-save" class="btn-accent">Save</button>
  </div>
</div>

<script src="./app.js"></script>
<script src="./status.js"></script>
<script src="./settings.js"></script>
</body>
</html>
```

- [ ] **Step 4: Write `frontend/dist/main.css`**

```css
:root {
  --bg: #F7F8FA; --card-bg: #FFFFFF; --border: #E6E8EB;
  --text: #101828; --secondary: #667085; --accent: #2F6FED;
  --green: #22C55E; --red: #E11D48;
}
:root[data-theme="dark"] {
  --bg: #1C1D1F; --card-bg: #232427; --border: #333438;
  --text: #F2F2F3; --secondary: #9AA0A6;
}

* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; overflow: hidden; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  background: transparent;
}
button { font-family: inherit; cursor: pointer; border: none; background: none; }
input { font-family: inherit; }

.page {
  width: 380px; height: 600px;
  background: var(--bg);
  border-radius: 14px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  box-shadow: 0 20px 60px rgba(0,0,0,0.28);
  border: 1px solid var(--border);
}

.titlebar {
  height: 46px; flex: 0 0 auto;
  display: flex; align-items: center; justify-content: space-between;
  padding: 0 14px; border-bottom: 1px solid var(--border);
  color: var(--text); font-size: 13px; font-weight: 600;
  -webkit-app-region: drag;
}
.titlebar-left { display: flex; align-items: center; gap: 8px; }
.icon-btn {
  -webkit-app-region: no-drag;
  display: flex; align-items: center; justify-content: center;
  width: 26px; height: 26px; border-radius: 7px; color: var(--secondary);
}
.icon-btn:hover { background: var(--card-bg); }

.body { flex: 1 1 auto; overflow-y: auto; padding: 22px 18px 8px; display: flex; flex-direction: column; gap: 18px; }

.hero { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 4px 0 2px; }
.status-ring {
  width: 60px; height: 60px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  border: 3px solid var(--secondary);
  color: var(--secondary);
}
.state-label { font-size: 17px; font-weight: 650; color: var(--text); letter-spacing: -0.2px; }
.sub-label { font-size: 13px; color: var(--secondary); text-align: center; }

.spin { animation: oft-spin 1.1s linear infinite; }
@keyframes oft-spin { to { transform: rotate(360deg); } }

.btn-primary {
  width: 100%; height: 40px; border-radius: 10px;
  background: var(--accent); color: #FFFFFF;
  font-size: 14px; font-weight: 600;
}
.btn-primary.secondary { background: transparent; color: var(--text); border: 1px solid var(--border); }

.card { border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); }
.details-card { padding: 14px 16px; display: grid; grid-template-columns: auto 1fr; row-gap: 10px; column-gap: 14px; font-size: 13px; }
.details-card .k { color: var(--secondary); }
.details-card .v { color: var(--text); text-align: right; }

.section-label { font-size: 11px; font-weight: 600; letter-spacing: 0.6px; text-transform: uppercase; color: var(--secondary); margin-bottom: 8px; }
.activity-list { max-height: 128px; overflow-y: auto; }
.activity-row { display: flex; gap: 10px; padding: 8px 12px; border-bottom: 1px solid var(--border); }
.activity-row:last-child { border-bottom: none; }
.activity-row .t { font-family: ui-monospace, Menlo, monospace; font-size: 11px; color: var(--secondary); flex: 0 0 auto; }
.activity-row .m { font-size: 12px; color: var(--text); }

.footer {
  flex: 0 0 auto; height: 34px;
  display: flex; align-items: center; justify-content: space-between;
  padding: 0 14px; border-top: 1px solid var(--border);
  font-size: 11px; color: var(--secondary);
}
.update-badge { display: flex; align-items: center; gap: 5px; color: var(--accent); font-weight: 600; }
.update-badge .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); }

.tabs { flex: 0 0 auto; display: flex; gap: 4px; padding: 10px 14px 0; }
.tab { flex: 1; text-align: center; padding: 8px 0; font-size: 13px; font-weight: 600; color: var(--secondary); border-bottom: 2px solid transparent; }
.tab.active { color: var(--text); border-bottom-color: var(--accent); }

.pane { display: flex; flex-direction: column; gap: 14px; }
.field { display: flex; flex-direction: column; gap: 6px; font-size: 12px; color: var(--secondary); }
.field input {
  height: 34px; border-radius: 8px; border: 1px solid var(--border);
  background: var(--card-bg); color: var(--text); padding: 0 10px; font-size: 13px;
}

.toggle-row { display: flex; align-items: center; justify-content: space-between; padding: 12px 2px; border-bottom: 1px solid var(--border); }
.toggle-text { display: flex; flex-direction: column; gap: 2px; }
.toggle-text span:first-child { font-size: 13px; color: var(--text); }
.toggle-text .hint { font-size: 11px; color: var(--secondary); }
.switch { width: 38px; height: 22px; border-radius: 11px; background: var(--border); position: relative; flex: 0 0 auto; }
.switch.on { background: var(--accent); }
.switch .knob { width: 18px; height: 18px; border-radius: 50%; background: #FFFFFF; position: absolute; top: 2px; left: 2px; transition: left 0.15s; }
.switch.on .knob { left: 18px; }

.settings-error { font-size: 12px; color: var(--red); padding: 8px 0; }

.settings-footer { flex: 0 0 auto; display: flex; justify-content: flex-end; gap: 8px; padding: 14px; border-top: 1px solid var(--border); }
.btn-secondary { height: 32px; padding: 0 14px; border-radius: 8px; border: 1px solid var(--border); color: var(--text); font-size: 13px; font-weight: 600; }
.btn-accent { height: 32px; padding: 0 16px; border-radius: 8px; background: var(--accent); color: #FFFFFF; font-size: 13px; font-weight: 600; }
```

- [ ] **Step 5: Write `frontend/dist/app.js`** (shared init: theme, page nav, the Go-call helper)

```js
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
});
```

- [ ] **Step 6: Write `frontend/dist/status.js`**

```js
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
```

- [ ] **Step 7: Write `frontend/dist/settings.js`**

```js
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
```

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum wails.json frontend/
git commit -m "feat(ui): scaffold Wails frontend (status + settings pages)"
```

---

### Task 2: `Bridge` type + Wails app lifecycle

**Files:**
- Create: `cmd/openfortitray/wailsapp.go`
- Create: `cmd/openfortitray/wailsapp_test.go`
- Modify: `cmd/openfortitray/main.go` (replace the Qt lifecycle block; add `ctx context.Context` field to `app`)
- Delete: `cmd/openfortitray/qtapp.go`, `cmd/openfortitray/qtapp_test.go`

**Interfaces:**
- Consumes: `a.pump()`, `a.Connect()`, `a.Disconnect()` (unchanged bodies, from Task 3 onward `a.pump` changes).
- Produces: `type Bridge struct { a *app }` with exported methods `Connect()`, `Disconnect()`, `ShowSettings()`, `ShowStatus()`, `Quit()`, `HideWindow()`, `IsDarkMode() bool` — later tasks add `CurrentView()`, `GetConfig()`, `SaveConfig()` to this same type. `buildAppOptions(a *app, assets embed.FS) *options.App` — the testable seam for window config.

- [ ] **Step 1: Write the failing test**

```go
// cmd/openfortitray/wailsapp_test.go
package main

import (
	"embed"
	"testing"
)

//go:embed testdata_wailsapp/dummy.txt
var testAssets embed.FS

func TestBuildAppOptionsFrameless380x600(t *testing.T) {
	a := &app{}
	opts := buildAppOptions(a, testAssets)

	if !opts.Frameless {
		t.Error("expected Frameless: true")
	}
	if opts.Width != 380 || opts.Height != 600 {
		t.Errorf("expected 380x600, got %dx%d", opts.Width, opts.Height)
	}
	if opts.Title != "OpenFortiTray" {
		t.Errorf("expected title OpenFortiTray, got %q", opts.Title)
	}
	if len(opts.Bind) != 1 {
		t.Fatalf("expected exactly one bound object, got %d", len(opts.Bind))
	}
	if _, ok := opts.Bind[0].(*Bridge); !ok {
		t.Errorf("expected bound object to be *Bridge, got %T", opts.Bind[0])
	}
}
```

Create the embed fixture the test needs:

```bash
mkdir -p cmd/openfortitray/testdata_wailsapp
echo "fixture" > cmd/openfortitray/testdata_wailsapp/dummy.txt
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/openfortitray/ -run TestBuildAppOptionsFrameless380x600 -v`
Expected: FAIL — `buildAppOptions`/`Bridge` undefined.

- [ ] **Step 3: Write `cmd/openfortitray/wailsapp.go`**

```go
package main

import (
	"context"
	"embed"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/settings"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Bridge is the exported adapter Wails binds to the frontend. It holds no
// logic of its own — every method delegates to app, mirroring the existing
// tray.App/status.Host/settings.Host adapter pattern. app itself stays
// unexported; Wails requires an exported bound type, so Bridge is that type.
type Bridge struct {
	a *app
}

func (b *Bridge) Connect()      { b.a.Connect() }
func (b *Bridge) Disconnect()   { b.a.Disconnect() }
func (b *Bridge) ShowSettings() { b.a.ShowSettings() }
func (b *Bridge) ShowStatus()   { b.a.ShowStatus() }
func (b *Bridge) Quit()         { b.a.Quit() }

func (b *Bridge) HideWindow() {
	if b.a.ctx != nil {
		runtime.WindowHide(b.a.ctx)
	}
}

func (b *Bridge) IsDarkMode() bool { return isDarkMode() }

func (b *Bridge) CurrentView() uistate.View {
	return uistate.ViewFor(b.a.snapshot())
}

func (b *Bridge) GetConfig() config.Config {
	return *b.a.settingsHost().Config()
}

func (b *Bridge) SaveConfig(cfg config.Config) *settings.Issue {
	if issue := settings.Validate(&cfg); issue != nil {
		return issue
	}
	if err := b.a.settingsHost().Commit(&cfg); err != nil {
		return &settings.Issue{Message: err.Error()}
	}
	return nil
}

// buildAppOptions is the testable seam between app/Bridge construction and
// wails.Run: it returns the options struct without invoking the webview, so
// its shape (frameless, fixed size, bound object) is unit-testable.
func buildAppOptions(a *app, assets embed.FS) *options.App {
	bridge := &Bridge{a: a}
	return &options.App{
		Title:            "OpenFortiTray",
		Width:            380,
		Height:           600,
		Frameless:        true,
		DisableResize:    true,
		HideWindowOnClose: true,
		AssetServer: &AssetServerOptions{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			a.ctx = ctx
		},
		Bind: []interface{}{bridge},
	}
}
```

`b.a.settingsHost()` and `b.a.snapshot()` are placeholders for methods Task 4/existing code already provide — Task 4 adds `settingsHost()` (a thin accessor returning the existing `settings.Host`-shaped view of `a`); `snapshot()` already exists per `main.go:221-231` per the earlier investigation. If `AssetServerOptions` differs from the real Wails v2 type name, the implementer resolves it via `go doc github.com/wailsapp/wails/v2/pkg/options.App` — this is the one place in this plan where the exact wails v2 struct field name should be confirmed against the installed version before compiling; the shape (an assets embed.FS) is stable across v2 releases even if the field/type name varies slightly.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/openfortitray/ -run TestBuildAppOptionsFrameless380x600 -v`
Expected: PASS (after resolving the exact `options.App` asset-server field per the note above).

- [ ] **Step 5: Replace `main()`'s Qt lifecycle block**

In `cmd/openfortitray/main.go`, delete the block from `qtApp := newQApplication(os.Args)` (main.go:1623) through `execQApplication()` / `a.awaitShutdown()` (main.go:1828-1834) and replace with:

```go
//go:embed all:frontend/dist
var frontendAssets embed.FS

func main() {
	// ... existing config/tunnel/app construction above is unchanged ...

	go a.pump()
	go a.startUpdateChecker(...) // unchanged call, keep existing arguments

	if err := wails.Run(buildAppOptions(a, frontendAssets)); err != nil {
		log.Fatalf("wails run: %v", err)
	}
	a.awaitShutdown()
}
```

Add `ctx context.Context` as a new field on the `app` struct (main.go:52-210 per the earlier investigation), and add `"context"`, `"embed"`, `"github.com/wailsapp/wails/v2"` to imports.

- [ ] **Step 6: Delete Qt app-lifecycle leftovers**

```bash
rm cmd/openfortitray/qtapp.go cmd/openfortitray/qtapp_test.go
```

- [ ] **Step 7: Commit**

```bash
git add cmd/openfortitray/wailsapp.go cmd/openfortitray/wailsapp_test.go \
  cmd/openfortitray/testdata_wailsapp cmd/openfortitray/main.go
git rm cmd/openfortitray/qtapp.go cmd/openfortitray/qtapp_test.go
git commit -m "feat(ui): add Wails Bridge + app lifecycle, remove qtapp.go"
```

---

### Task 3: Remove `internal/uidispatch`; wire `a.pump()` to `EventsEmit`

**Files:**
- Modify: `cmd/openfortitray/main.go` (`pump()`, every `a.dispatch.Post`/`PostAndWait` call site, remove `dispatch *uidispatch.Queue` field)
- Delete: `internal/uidispatch/` (package + its test file)

**Interfaces:**
- Consumes: `runtime.EventsEmit(ctx context.Context, eventName string, data ...interface{})` from `github.com/wailsapp/wails/v2/pkg/runtime`.
- Produces: `a.pump()` now emits `"tunnel:event"` carrying `uistate.ViewFor(e)` instead of calling three `Apply()` methods.

- [ ] **Step 1: Find every `a.dispatch` call site**

```bash
grep -rn "a\.dispatch\." cmd/openfortitray/*.go
```

Every result is one of: (a) `a.pump()`'s own three-`Apply()` closure — rewritten below; (b) a closure that only mutates Go-side state or calls a `Bridge`-reachable method with no Qt widget touch — these become a direct, synchronous call (`a.dispatch.Post(f)` → `f()`); there is no "must run on the toolkit's main thread" constraint anymore, since nothing here touches a widget tree — Wails' `runtime.EventsEmit`/`WindowShow`/etc. are documented safe to call from any goroutine.

- [ ] **Step 2: Rewrite `pump()`**

```go
func (a *app) pump() {
	for e := range a.events {
		if a.quitting.Load() {
			continue
		}
		e := e
		a.notifyFor(e)
		if a.quitting.Load() {
			continue
		}
		a.setSnapshot(e) // existing method (main.go:221-231) — keep as-is
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "tunnel:event", uistate.ViewFor(e))
		}
		if a.onPermanentError != nil && e.State == tunnel.Error && strings.Contains(e.Detail, "install is broken") {
			a.onPermanentError()
		}
	}
}
```

- [ ] **Step 3: Rewrite `reportCheckResult`** (was a blocking `QMessageBox.Exec()`, now an event the frontend renders as a dismissible banner — no blocking, so the "stuck dialog blocks the whole queue" class of bug from the Qt era is structurally gone)

```go
func (a *app) reportCheckResult(heading, body string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "update:check-result", struct {
		Heading string `json:"heading"`
		Body    string `json:"body"`
	}{heading, body})
}
```

(Rendering this event in `status.js` is out of scope for this task — Task 6 adds the listener alongside the rest of the status page wiring; this task only needs the Go side to compile and stop depending on `uidispatch`/Qt.)

- [ ] **Step 4: Convert remaining call sites, remove the field**

For each remaining `a.dispatch.Post(func() { ... })` found in Step 1 (bootstrap hooks, `onSystemWake`, `onScreenWake`, `Quit()`), replace with the closure's body run directly (no `Post` wrapper). Remove `dispatch *uidispatch.Queue` from the `app` struct and its construction line in `main()`. Remove the `drain := qt.NewQTimer2(nil)` block (already deleted in Task 2, Step 5 — confirm it's gone).

- [ ] **Step 5: Delete the package**

```bash
rm -rf internal/uidispatch
```

- [ ] **Step 6: Run the full test suite**

Run: `CGO_ENABLED=1 go test ./...`
Expected: `cmd/openfortitray` and `internal/uidispatch` no longer referenced anywhere; remaining failures (if any) are pre-existing Qt-widget tests slated for deletion in Tasks 5-8, not new breakage from this task. Confirm via `go build ./...` that the non-test build is clean first — that isolates this task's own correctness from later tasks' pending deletions.

- [ ] **Step 7: Commit**

```bash
git add cmd/openfortitray/main.go
git rm -r internal/uidispatch
git commit -m "refactor(ui): replace uidispatch with direct calls + Wails EventsEmit"
```

---

### Task 4: `internal/settings` bridge — keep `logic.go`, drop the Qt render layer

**Files:**
- Create: `internal/settings/bridge.go`
- Create: `internal/settings/bridge_test.go`
- Delete: `internal/settings/settings.go`, `internal/settings/settings_test.go`
- Modify: `cmd/openfortitray/main.go` (add `func (a *app) settingsHost() Host`-equivalent accessor Task 2 referenced; see Step 3 below)

**Interfaces:**
- Produces: `func Validate(c *config.Config) *Issue` (exported wrapper around the existing private `validateConfig`) — the exact function `Bridge.SaveConfig` (Task 2) calls.
- Consumes: `internal/settings/logic.go`'s existing private `validateConfig(c *config.Config) error` and the existing exported `Issue`/`FirstConnectIssue` (logic.go:614, 638) — unchanged.

- [ ] **Step 1: Write the failing test**

```go
// internal/settings/bridge_test.go
package settings

import (
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/config"
)

func TestValidateReturnsIssueForBadPort(t *testing.T) {
	cfg := &config.Config{
		Profiles: []config.Profile{{Gateway: "vpn.example.com", Port: 0}},
	}
	issue := Validate(cfg)
	if issue == nil {
		t.Fatal("expected a validation issue for port 0, got nil")
	}
}

func TestValidateReturnsNilForGoodConfig(t *testing.T) {
	cfg := &config.Config{
		Profiles: []config.Profile{{Gateway: "vpn.example.com", Port: 443}},
	}
	if issue := Validate(cfg); issue != nil {
		t.Fatalf("expected no issue, got %+v", issue)
	}
}
```

(Adjust the exact zero-value trigger for a validation failure — e.g. `Port: 0` — to whatever `validateConfig`/`validatePortValue` in `logic.go:333` actually rejects; the implementer confirms this against `logic_test.go`'s existing table cases for `validatePortValue`/`validateProfile`, which already enumerate the exact invalid inputs.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/settings/ -run TestValidateReturns -v`
Expected: FAIL — `Validate` undefined.

- [ ] **Step 3: Write `internal/settings/bridge.go`**

```go
// Package settings' bridge.go is the one file a UI layer (Wails' Bridge, or
// any future one) calls into. Everything it wraps already exists in
// logic.go, unexported because until now only this package's own Qt render
// layer called it.
package settings

import "github.com/savvaskoualis/openfortitray/internal/config"

// Validate runs the same checks Commit always ran internally, exposed for a
// UI layer to call before committing so it can show a field-level error
// instead of a generic failure. Returns nil when c is valid.
func Validate(c *config.Config) *Issue {
	if err := validateConfig(c); err != nil {
		return &Issue{Message: err.Error()}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/settings/ -run TestValidateReturns -v`
Expected: PASS.

- [ ] **Step 5: Delete the Qt render layer**

```bash
rm internal/settings/settings.go internal/settings/settings_test.go
```

- [ ] **Step 6: Give `Bridge.GetConfig`/`SaveConfig` (Task 2) something to call**

`Bridge` needs `a.settingsHost()` to return something with `Config() *config.Config` and `Commit(c *config.Config) error` — exactly the `settings.Host` interface `app` already satisfies (per `main.go`'s existing `Config()`/`Commit()` methods, unaffected by this task). Since `settings.Host` lived in the now-deleted `settings.go`, move its declaration into `bridge.go`:

```go
// Host is implemented by the app adapter (cmd/openfortitray/main.go) and is
// the only way this package's consumers reach live configuration.
type Host interface {
	Config() *config.Config
	Commit(c *config.Config) error
	Connect()
	Disconnect()
}
```

In `cmd/openfortitray/main.go`, add a trivial accessor so `Bridge` doesn't need to know `app` already satisfies `settings.Host` structurally:

```go
func (a *app) settingsHost() settings.Host { return a }
```

- [ ] **Step 7: Run the full test suite for this package**

Run: `go test ./internal/settings/...`
Expected: `logic_test.go`'s ~995 lines pass unchanged; `bridge_test.go` passes; no reference to the deleted `settings_test.go` remains.

- [ ] **Step 8: Commit**

```bash
git add internal/settings/bridge.go internal/settings/bridge_test.go cmd/openfortitray/main.go
git rm internal/settings/settings.go internal/settings/settings_test.go
git commit -m "refactor(settings): extract Validate/Host into bridge.go, drop Qt render layer"
```

---

### Task 5: Rewrite `internal/tray` on `energye/systray`

**Files:**
- Modify: `internal/tray/tray.go` (full rewrite of widget construction; `App` interface, `icons.go`, `badge.go` untouched)
- Delete: `internal/tray/tray_test.go`
- Create: `internal/tray/tray_test.go` (new, pure-logic version)

**Interfaces:**
- Consumes: `internal/tray/icons.go`'s `iconGray`/`iconGreen`/`iconYellow`/`iconRed` `[]byte` vars, `badge.go`'s `composeBadge([]byte) ([]byte, error)` and `padToSquare([]byte) ([]byte, error)` — all unchanged.
- Produces: `func Setup(a App) (*Controller, error)` (same signature shape as today), `func (c *Controller) Apply(e tunnel.Event)` (same signature as today, per `tray.go:241`), `func (c *Controller) iconForCurrent() []byte` (was `*qt.QIcon`, now raw bytes — the seam Step 1's test targets).

- [ ] **Step 1: Write the failing test** (pure logic — no systray runtime needed, mirroring how `logic_test.go` tests `internal/settings` without a widget)

```go
// internal/tray/tray_test.go
package tray

import (
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

func TestIconForCurrentMatchesKind(t *testing.T) {
	c := &Controller{}
	c.currentKind = uistate.KindOK
	icon, err := c.iconForCurrent()
	if err != nil {
		t.Fatalf("iconForCurrent: %v", err)
	}
	if len(icon) == 0 {
		t.Fatal("expected non-empty icon bytes")
	}
}

func TestApplyUpdatesCurrentKind(t *testing.T) {
	c := &Controller{}
	c.Apply(tunnel.Event{State: tunnel.Connected})
	if c.currentKind != uistate.KindOK {
		t.Errorf("expected KindOK after Connected event, got %v", c.currentKind)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tray/ -v`
Expected: FAIL — old `tray_test.go` (Qt-based) still present and won't compile once `tray.go` changes; delete it now before proceeding (this task replaces it wholesale, unlike other tasks' strict test-first order, because the old and new tests cannot coexist against the same `Controller` type mid-rewrite):

```bash
rm internal/tray/tray_test.go
```

Re-run with only the new test file present: FAIL — `iconForCurrent` returns `*qt.QIcon` today, not `([]byte, error)`; `Controller` has no exported zero-value-constructible shape yet compatible with `&Controller{}`.

- [ ] **Step 3: Rewrite `internal/tray/tray.go`**

```go
package tray

import (
	"github.com/energye/systray"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

// App is unchanged from before — see the interface block below, identical
// to the pre-Wails version.
type App interface {
	Connect()
	Disconnect()
	SetAutostart(on bool) error
	AutostartEnabled() bool
	LogPath() string
	Version() string
	ShowSettings()
	ShowStatus()
	Quit()
	UpdateClicked()
}

type Controller struct {
	app         App
	currentKind uistate.Kind
	lastView    uistate.View

	mStatus  *systray.MenuItem
	mAction  *systray.MenuItem
	mUpdate  *systray.MenuItem
}

// Setup builds the tray icon and menu and starts systray's own run loop in
// a background goroutine (systray.Run blocks its caller by design; running
// it here rather than in main() keeps main() symmetrical with the rest of
// the Wails startup sequence in cmd/openfortitray/main.go).
func Setup(app App) (*Controller, error) {
	c := &Controller{app: app, currentKind: uistate.KindIdle}
	ready := make(chan struct{})
	go systray.Run(func() {
		icon, _ := c.iconForCurrent()
		systray.SetIcon(icon)
		systray.SetTooltip("OpenFortiTray")

		c.mStatus = systray.AddMenuItem("Not Connected", "")
		c.mStatus.Disable()
		systray.AddSeparator()
		c.mAction = systray.AddMenuItem("Connect", "")
		mSettings := systray.AddMenuItem("Settings", "")
		mUpdateItem := systray.AddMenuItem("Check for Updates", "")
		c.mUpdate = mUpdateItem
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit", "")

		systray.SetOnClick(func(menu systray.IMenu) { app.ShowStatus() })

		go func() {
			for {
				select {
				case <-c.mAction.ClickedCh:
					if c.lastView.CanDisconnect {
						app.Disconnect()
					} else if c.lastView.CanConnect {
						app.Connect()
					}
				case <-mSettings.ClickedCh:
					app.ShowSettings()
				case <-mUpdateItem.ClickedCh:
					app.UpdateClicked()
				case <-mQuit.ClickedCh:
					app.Quit()
				}
			}
		}()

		close(ready)
	}, func() {})
	<-ready
	return c, nil
}

// Apply renders one tunnel event onto the tray: icon, status label, and the
// connection action's label/target. Same contract as the pre-Wails version
// (tray.go:241 in the Qt implementation).
func (c *Controller) Apply(e tunnel.Event) {
	v := uistate.ViewFor(e)
	c.currentKind = v.Kind
	c.lastView = v
	icon, err := c.iconForCurrent()
	if err == nil {
		systray.SetIcon(icon)
	}
	if c.mStatus != nil {
		c.mStatus.SetTitle(v.MenuLabel)
	}
	if c.mAction != nil {
		if v.CanDisconnect {
			c.mAction.SetTitle("Disconnect")
		} else {
			c.mAction.SetTitle("Connect")
		}
	}
}

func (c *Controller) SetTooltip(s string) { systray.SetTooltip(s) }

// iconForCurrent returns the padded PNG bytes for c.currentKind, reusing
// icons.go/badge.go's existing pure image logic unchanged.
func (c *Controller) iconForCurrent() ([]byte, error) {
	raw := iconGray
	switch c.currentKind {
	case uistate.KindOK:
		raw = iconGreen
	case uistate.KindBusy:
		raw = iconYellow
	case uistate.KindBad:
		raw = iconRed
	}
	return padToSquare(raw)
}
```

Note for the implementer: `energye/systray`'s exact API (`SetOnClick`, `IMenu`, `ClickedCh` vs a callback-based `Click(func())`) should be confirmed with `go doc github.com/energye/systray` against the version Task 1 pinned — the shape above reflects the documented v1.0.x API but menu-click wiring is the one place worth a quick doc-check before trusting it compiles as written.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tray/ -v`
Expected: PASS.

- [ ] **Step 5: Wire `Setup`'s call site in `main.go`**

The existing call `ctrl, err := tray.Setup(a)` (main.go:1649) keeps its exact call shape — `App` interface unchanged, so no caller-side edit needed beyond confirming it still compiles against the rewritten `Controller`.

- [ ] **Step 6: Commit**

```bash
git add internal/tray/tray.go internal/tray/tray_test.go
git commit -m "refactor(tray): rebuild on energye/systray, drop Qt widget code"
```

---

### Task 6: Status page live wiring (event stream + activity log)

**Files:**
- Modify: `cmd/openfortitray/wailsapp.go` (`Bridge.CurrentView` already added in Task 2 — this task adds `Bridge.RecentActivity`)
- Modify: `cmd/openfortitray/main.go` (a small ring buffer of recent events, reusing `internal/uistate.Ring`/`Entry`, per `uistate.go:137-159`, already used by the old `internal/status` package the same way)
- Modify: `frontend/dist/status.js` (wire `OFT.renderActivity`, added in Task 1, to real data + the `"update:check-result"` event from Task 3)

**Interfaces:**
- Consumes: `uistate.Ring`/`uistate.Entry` (unchanged).
- Produces: `func (b *Bridge) RecentActivity() []uistate.Entry`.

- [ ] **Step 1: Write the failing test**

```go
// cmd/openfortitray/wailsapp_test.go (append)
func TestRecentActivityReturnsRingContents(t *testing.T) {
	a := &app{}
	a.activity = uistate.NewRing(10) // matches the constructor internal/status used before deletion — confirm exact name via `grep -n "func New" internal/uistate/uistate.go`
	a.activity.Add(uistate.Entry{Text: "Tunnel established"})
	b := &Bridge{a: a}

	got := b.RecentActivity()
	if len(got) != 1 || got[0].Text != "Tunnel established" {
		t.Errorf("expected one entry 'Tunnel established', got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/openfortitray/ -run TestRecentActivity -v`
Expected: FAIL — `a.activity` field and `Bridge.RecentActivity` undefined.

- [ ] **Step 3: Add the field and method**

Add `activity *uistate.Ring` to the `app` struct in `main.go`, construct it alongside the other fields in `main()` (same ring size the old `internal/status` package used — check its deleted `status.go` in git history via `git show HEAD~N:internal/status/status.go` if the size constant isn't obvious from `uistate.go` itself), and append to it inside `pump()` right after `a.setSnapshot(e)`:

```go
a.activity.Add(uistate.Entry{At: time.Now(), Text: e.Detail})
```

```go
// cmd/openfortitray/wailsapp.go (add to Bridge)
func (b *Bridge) RecentActivity() []uistate.Entry {
	if b.a.activity == nil {
		return nil
	}
	return b.a.activity.Entries() // confirm exact accessor name on uistate.Ring; add one if it doesn't exist, following the same pattern the old internal/status package used to read the ring
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/openfortitray/ -run TestRecentActivity -v`
Expected: PASS.

- [ ] **Step 5: Wire the frontend**

In `frontend/dist/status.js`, inside the `DOMContentLoaded` handler added in Task 1, add:

```js
OFT.call("RecentActivity").then((entries) => {
  OFT.renderActivity((entries || []).map((e) => ({ time: new Date(e.At).toLocaleTimeString(), msg: e.Text })));
});

if (window.runtime && window.runtime.EventsOn) {
  window.runtime.EventsOn("update:check-result", (r) => {
    alert(r.heading + "\n\n" + r.body); // placeholder-free minimal UI: a native alert is acceptable here since this event is rare (manual "Check for Updates" only) and matches the old QMessageBox's modal-blocking-free intent without adding a new modal component to the page for a once-in-a-while action
  });
}
```

- [ ] **Step 6: Commit**

```bash
git add cmd/openfortitray/wailsapp.go cmd/openfortitray/wailsapp_test.go \
  cmd/openfortitray/main.go frontend/dist/status.js
git commit -m "feat(ui): wire activity log + update-check event to the status page"
```

---

### Task 7: Tray-relative window positioning + multi-monitor clamp

**Files:**
- Modify: `internal/tray/tray.go` (`SetOnClick` handler computes position instead of only calling `ShowStatus`)
- Create: `internal/tray/position.go`
- Create: `internal/tray/position_test.go`

**Interfaces:**
- Produces: `func clampToWorkArea(x, y, w, h, workX, workY, workW, workH int) (int, int)` — pure, fully unit-testable without any windowing system.

- [ ] **Step 1: Write the failing test**

```go
// internal/tray/position_test.go
package tray

import "testing"

func TestClampToWorkAreaKeepsWindowOnScreen(t *testing.T) {
	// Window would spawn 40px off the right edge of a 1920-wide work area.
	x, y := clampToWorkArea(1900, 20, 380, 600, 0, 0, 1920, 1080)
	if x+380 > 1920 {
		t.Errorf("window right edge %d exceeds work area width 1920", x+380)
	}
	if x < 0 {
		t.Errorf("clamp produced negative x: %d", x)
	}
	if y < 0 {
		t.Errorf("clamp produced negative y: %d", y)
	}
}

func TestClampToWorkAreaNoOpWhenAlreadyFits(t *testing.T) {
	x, y := clampToWorkArea(100, 40, 380, 600, 0, 0, 1920, 1080)
	if x != 100 || y != 40 {
		t.Errorf("expected no-op (100,40), got (%d,%d)", x, y)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tray/ -run TestClampToWorkArea -v`
Expected: FAIL — `clampToWorkArea` undefined.

- [ ] **Step 3: Write `internal/tray/position.go`**

```go
package tray

// clampToWorkArea moves a w×h window whose top-left would be (x,y) so it
// stays fully inside the work area [workX,workY, workX+workW, workY+workH].
// Used to keep the popover on-screen when the tray icon sits near a
// monitor's edge (a real bug class with tray-relative positioning:
// spawning partly or fully off a disconnected second monitor).
func clampToWorkArea(x, y, w, h, workX, workY, workW, workH int) (int, int) {
	if x+w > workX+workW {
		x = workX + workW - w
	}
	if x < workX {
		x = workX
	}
	if y+h > workY+workH {
		y = workY + workH - h
	}
	if y < workY {
		y = workY
	}
	return x, y
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tray/ -run TestClampToWorkArea -v`
Expected: PASS.

- [ ] **Step 5: Wire it into the click handler**

In `internal/tray/tray.go`'s `Setup`, `Controller` needs a `ctx context.Context` and a `showAt func(x, y int)` callback the app side provides (since `runtime.WindowSetPosition`/`WindowShow` need a Wails context that `internal/tray` — a package with no Wails import today — shouldn't take on directly). Add:

```go
// Controller (add field)
showAt func(x, y int)

// Setup (add parameter)
func Setup(app App, showAt func(x, y int)) (*Controller, error) {
	c := &Controller{app: app, currentKind: uistate.KindIdle, showAt: showAt}
	// ... unchanged ...
	systray.SetOnClick(func(menu systray.IMenu) {
		x, y := systray.GetIconPosition() // confirm exact accessor name via `go doc github.com/energye/systray` — energye's fork exposes icon geometry unlike upstream getlantern/systray, which is exactly why this fork was chosen in the design spec
		cx, cy := clampToWorkArea(x, y, 380, 600, 0, 0, 1920, 1080) // work-area bounds: the implementer replaces the hardcoded 1920x1080 with the real primary-display work area, queried via runtime.ScreenGetAll(ctx) on the app side and passed through showAt's closure — see Step 6
		if c.showAt != nil {
			c.showAt(cx, cy)
		}
		app.ShowStatus()
	})
	// ...
}
```

- [ ] **Step 6: Provide `showAt` from `main.go`**

```go
ctrl, err := tray.Setup(a, func(x, y int) {
	if a.ctx != nil {
		runtime.WindowSetPosition(a.ctx, x, y)
		runtime.WindowShow(a.ctx)
	}
})
```

Replace the hardcoded `1920, 1080` from Step 5 with a real work-area lookup inside this closure (`runtime.ScreenGetAll(a.ctx)`, picking the screen containing `(x, y)`) before calling `clampToWorkArea` — the pure function stays exactly as tested; only its caller in `main.go` gets the real numbers.

- [ ] **Step 7: Commit**

```bash
git add internal/tray/position.go internal/tray/position_test.go internal/tray/tray.go cmd/openfortitray/main.go
git commit -m "feat(tray): clamp popover position to the current monitor's work area"
```

---

### Task 8: Delete remaining Qt-only packages/files; simplify `app`; drop `miqt`

**Files:**
- Delete: `internal/shell/` (+ `shell_test.go`), `internal/status/` (+ `status_test.go`), `internal/uitheme/` (+ `uitheme_test.go`)
- Delete: `cmd/openfortitray/glass_darwin.m`, `cmd/openfortitray/glass_windows.go`, `cmd/openfortitray/glass.go`
- Delete: `cmd/openfortitray/update_prompt_test.go` (Qt `QDialog`-based; the `"update:check-result"` event from Task 3/6 replaces this UI, with no equivalent Go-side dialog-state to test)
- Modify: `cmd/openfortitray/main.go` (remove `win`, `shell`, `status` fields and every reference to them; remove now-dead `installBootstrapHooks`/`watchDockActivation` calls that only existed to parent a Qt dialog on `a.win` — replace with direct calls per Task 3's pattern)
- Modify: `go.mod`, `go.sum` (drop `github.com/mappu/miqt`)

**Interfaces:** none new — this task only removes dead surface Tasks 2-7 already made unreachable.

- [ ] **Step 1: Confirm nothing still references the doomed packages**

```bash
grep -rln "internal/shell\|internal/status\|internal/uitheme" --include="*.go" . | grep -v _test.go
```

Expected: empty, or only the files this task is about to delete. If `main.go` still constructs `a.shell`/`a.status`, remove those lines now (they were only ever consumed by the Qt widget tree, which no longer exists after Task 2's lifecycle rewrite).

- [ ] **Step 2: Delete the packages and Qt-only files**

```bash
git rm -r internal/shell internal/status internal/uitheme
git rm cmd/openfortitray/glass_darwin.m cmd/openfortitray/glass_windows.go cmd/openfortitray/glass.go
git rm cmd/openfortitray/update_prompt_test.go
```

- [ ] **Step 3: Clean up `main.go`'s `app` struct**

Remove `win *qt.QMainWindow`, `shell *shell.Shell`, `status *status.Controller` fields. Search for every remaining reference (`grep -n "a\.win\|a\.shell\|a\.status" cmd/openfortitray/main.go`) and remove or inline each — most are dialog-parenting calls (`bootstrap_darwin.go`'s privileged-helper install dialog, `dockpolicy_darwin.go`'s reopen-click) that become either a `runtime.EventsEmit` (if the frontend needs to show something) or a direct log line (if the Qt dialog was purely informational and the equivalent Wails-era UX is a tray notification via the OS notifier already used elsewhere in this codebase, per `a.notify`/`lastNotified` fields already on `app`).

- [ ] **Step 4: Drop `miqt` from `go.mod`**

```bash
go mod tidy
grep -q "mappu/miqt" go.mod && echo "STILL PRESENT — find the remaining import" || echo "clean"
```

- [ ] **Step 5: Full build + test pass**

Run: `CGO_ENABLED=1 go build ./...` — expect success with `CGO_ENABLED=1` still needed only if `energye/systray` uses cgo on the target platform (confirm via `go list -deps ./internal/tray/... | grep -i cgo`); if not, drop the `CGO_ENABLED=1 CGO_CXXFLAGS=-std=c++17` requirement from any remaining developer-facing docs (`README.md`, `Makefile`) since it was purely a `miqt`/Qt6 C++17 requirement.

Run: `go test ./...` — expect a fully green suite: `internal/settings` (logic + bridge), `internal/tray` (new pure tests), `cmd/openfortitray` (notify, lock, update_prompt removed, main_test.go's surviving pure-logic tests per the earlier investigation).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/openfortitray/main.go
git commit -m "chore: remove Qt/miqt entirely — shell, status, uitheme, glass code deleted"
```

---

### Task 9: CI + installer updates

**Files:**
- Modify: `.github/workflows/release.yml` (`build-darwin`, `build-windows`, `build-linux` jobs)
- Modify: `scripts/openfortitray.iss`
- Modify: `README.md` / install docs (Linux `webkit2gtk` dependency note)

**Interfaces:** none — CI/packaging only.

- [ ] **Step 1: Install the Wails CLI in each build job**

Add, as the first step after "Set up Go" in `build-darwin`, `build-windows`, `build-linux`:

```yaml
- name: Install Wails CLI
  run: go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2
```

- [ ] **Step 2: Remove Qt-specific steps**

In `build-darwin`, delete the "Verify Xcode Command Line Tools" and "Install Qt6" steps (Wails' macOS build needs Xcode's build tools implicitly via `go build`/cgo for the webview binding, already present on the `macos-latest` runner — no separate install step is needed; only add one back if `wails build` itself fails on missing tools).

In `build-windows`, delete "Set up MinGW-w64 + Qt6 (MSYS2 UCRT64)", "Put MinGW-w64 gcc on PATH", "Verify C toolchain", "Bundle Qt6 runtime DLLs", "Bundle transitive Qt6/MinGW DLL closure". Keep "Embed Windows manifest + icon", "Bundle openconnect", "Verify Inno Setup present", "Build Windows installer (Inno Setup)".

In `build-linux`, delete "Install Qt6 build deps"; add:

```yaml
- name: Install WebKitGTK build deps
  run: sudo apt-get update && sudo apt-get install -y libwebkit2gtk-4.1-dev libgtk-3-dev
```

- [ ] **Step 3: Replace the build step with `wails build`**

Replace each platform's "Build {darwin,windows,linux} binary" step's `go build ...` invocation with:

```yaml
- name: Build with Wails
  run: wails build -platform ${{ matrix.platform }} -o openfortitray${{ matrix.ext }}
```

(exact `-platform`/matrix values follow whatever `GOOS`/`GOARCH` matrix the job already defines — the implementer keeps the existing matrix structure, only swapping the build command itself.)

- [ ] **Step 4: Add the WebView2 bootstrapper to the Windows installer**

In `scripts/openfortitray.iss`, add under `[Files]`:

```ini
Source: "{#MyWailsDir}\MicrosoftEdgeWebview2Setup.exe"; DestDir: "{tmp}"; Flags: dontcopy
```

and under `[Run]` (create the section if it doesn't exist), before the app's own post-install run line:

```ini
Filename: "{tmp}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent /install"; StatusMsg: "Installing WebView2 Runtime..."; Check: not IsWebView2Installed
```

`IsWebView2Installed` is a `[Code]` section function checking the registry key `HKLM\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}` — add it to the existing `[Code]` section (or create one) per Inno Setup's standard WebView2-detection idiom.

- [ ] **Step 5: Replace the `[Files]` `MyQtDir` block**

Remove the `Source: "{#MyQtDir}\*.dll"` and `Source: "{#MyQtDir}\platforms\*"` lines entirely — Wails produces one self-contained binary with no separate runtime DLL tree. Remove the `MyQtDir` preprocessor define. `MyAppExe` and `MyOcDir` (openconnect bundle) stay unchanged.

- [ ] **Step 6: Document the Linux runtime dependency**

Add one line to the Linux install instructions (wherever `openconnect` is already documented as a prerequisite): `libwebkit2gtk-4.1-0` (or the distro's equivalent package name) is required at runtime; already present on GNOME/KDE desktop installs.

- [ ] **Step 7: Commit**

```bash
git add .github/workflows/release.yml scripts/openfortitray.iss README.md
git commit -m "ci: replace Qt6 bundling with wails build; add WebView2 bootstrap"
```

---

## Final Verification

After Task 9, run the full suite one more time from a clean checkout state:

```bash
go build ./...
go test ./...
wails build -platform darwin/arm64
```

Then follow the `run` skill's Electron/webview pattern to launch the built binary, click Connect (against a real or stubbed gateway), open Settings via the gear icon, and confirm the window matches the approved mock — this is the point where a real screenshot comparison against `https://claude.ai/artifact/JwDhoadtbhXFoU1vauQH6i` replaces any remaining assumption in this plan.
