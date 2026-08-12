package settings

import (
	"errors"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// Host is what the settings window needs from the application. Every method is
// called on the fyne UI goroutine (from a widget Action or from Apply, which the
// event pump marshals through fyne.Do).
type Host interface {
	// Config returns the live configuration. The window edits a deep copy and
	// only writes back through Commit.
	Config() *config.Config
	// Commit takes ownership of c, syncs the OS autostart login item to
	// c.Autostart, persists c to disk, and makes it the live config. c is an
	// independent copy the window will not mutate further.
	Commit(c *config.Config) error
	// Connect and Disconnect drive the tunnel, exactly as the tray items do.
	Connect()
	Disconnect()
}

// Controller owns the settings window and every widget in it. It is built once
// at startup (New) and reused: Show/Hide toggle visibility, the red close button
// is intercepted to Hide (never quit), and Apply feeds the live status strip
// from the one event pump. All methods run on the UI goroutine.
type Controller struct {
	host Host
	win  fyne.Window

	// work is the in-memory copy being edited; nothing reaches the live config
	// until Save/Commit. sel is the index into work.Profiles shown in the form.
	work *config.Config
	sel  int

	list *widget.List

	// tabs is the Basic/Advanced container; ShowIssue switches it to the tab an
	// issue lives on. banner is the persistent inline strip at the top of the
	// window that names a blocking Connect issue and its fix; it is hidden until
	// ShowIssue raises it and stays up until dismissed or a successful Save.
	tabs        *container.AppTabs
	banner      *fyne.Container
	bannerLabel *widget.Label

	nameEntry     *widget.Entry
	gatewayEntry  *widget.Entry
	customPort    *widget.Check
	portEntry     *widget.Entry
	authSelect    *widget.Select
	authNote      *widget.Label
	usernameEntry *widget.Entry
	certPathEntry *widget.Entry
	realmEntry    *widget.Entry
	autoConnect   *widget.Check
	keepAlive     *widget.Check

	// Advanced tab.
	dualStack       *widget.Check
	dtls            *widget.Check
	rememberSession *widget.Check
	certMode        *widget.RadioGroup
	certPin         *widget.Entry
	certPinItem     *widget.FormItem
	splitDNS        *widget.Entry
	samlPortEntry   *widget.Entry
	openconnectPath *widget.Entry
	helperPath      *widget.Entry

	statusText    *canvas.Text
	connectBtn    *widget.Button
	disconnectBtn *widget.Button

	// loading suppresses the widgets' OnChanged handlers while loadProfile
	// populates them, so repainting the form for a newly selected profile does
	// not write those values straight back into the working copy.
	loading bool
}

// New builds the settings window on the given (not-yet-shown) window and wires
// it to host. The window is left hidden; the tray's Settings… item calls Show.
func New(host Host, win fyne.Window) *Controller {
	c := &Controller{host: host, win: win}
	// Populate the working copy before build: SetContent renders the Form, which
	// runs the entry validators immediately, and the name validator reads
	// c.work. reset() then repaints list + form with the loaded values.
	c.work = cloneConfig(host.Config())
	c.sel = c.indexOf(c.work.ActiveProfile)
	c.build()
	c.reset()
	return c
}

// Show refreshes the working copy from the live config (discarding any edits
// left from a previous session) and reveals the window, focused.
func (c *Controller) Show() {
	c.reset()
	c.win.Show()
	c.win.RequestFocus()
}

// Apply renders one tunnel event onto the bottom status strip and mirrors the
// tray's Connect/Disconnect enabling. It is called only from inside fyne.Do
// (the shared event pump), so it is already on the UI goroutine and mutates
// widgets directly. Updating a hidden window's widgets is safe and cheap.
func (c *Controller) Apply(e tunnel.Event) {
	text, kind, active := statusFor(e)
	c.statusText.Text = text
	c.statusText.Color = colorFor(kind)
	c.statusText.Refresh()
	if active {
		c.connectBtn.Disable()
		c.disconnectBtn.Enable()
	} else {
		c.connectBtn.Enable()
		c.disconnectBtn.Disable()
	}
}

// reset discards the working copy and rebuilds it from the live config, then
// repaints the list and the form. Used on Show and on Cancel.
func (c *Controller) reset() {
	c.work = cloneConfig(c.host.Config())
	c.sel = c.indexOf(c.work.ActiveProfile)
	c.list.UnselectAll()
	c.list.Refresh()
	c.list.Select(c.sel)
	c.loadProfile(c.sel)
}

// indexOf returns the index of the named profile, or 0 (there is always at
// least one profile in a saved config).
func (c *Controller) indexOf(name string) int {
	for i := range c.work.Profiles {
		if c.work.Profiles[i].Name == name {
			return i
		}
	}
	return 0
}

func (c *Controller) build() {
	c.buildList()
	form := c.buildBasicTab()
	advanced := c.buildAdvancedTab()
	c.tabs = container.NewAppTabs(
		container.NewTabItem("Basic", form),
		container.NewTabItem("Advanced", advanced),
	)
	c.buildBanner()

	addBtn := widget.NewButton("Add", c.addProfile)
	dupBtn := widget.NewButton("Duplicate", c.duplicateProfile)
	delBtn := widget.NewButton("Delete", c.deleteProfile)
	activeBtn := widget.NewButton("Set active", c.setActive)
	listButtons := container.NewGridWithColumns(2, addBtn, dupBtn, delBtn, activeBtn)
	left := container.NewBorder(nil, listButtons, nil, nil, c.list)

	bottom := c.buildActionStrip()

	// The banner sits at the top; while hidden BorderLayout gives it no space.
	content := container.NewBorder(c.banner, bottom, left, nil, c.tabs)
	c.win.SetContent(content)
	c.win.Resize(fyne.NewSize(680, 460))
	// The red close button hides the window; the app only ever exits via the
	// tray's Quit item. Without this, closing the first-shown window would quit
	// the whole app (fyne's master-window rule).
	c.win.SetCloseIntercept(c.win.Hide)
}

func (c *Controller) buildList() {
	c.list = widget.NewList(
		func() int { return len(c.work.Profiles) },
		func() fyne.CanvasObject { return widget.NewLabel("template") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			lbl := o.(*widget.Label)
			name := c.work.Profiles[i].Name
			if name == c.work.ActiveProfile {
				name = "● " + name // active-profile marker
			}
			lbl.SetText(name)
		},
	)
	c.list.OnSelected = func(id widget.ListItemID) {
		c.sel = id
		c.loadProfile(id)
	}
}

func (c *Controller) buildBasicTab() *widget.Form {
	c.nameEntry = widget.NewEntry()
	c.nameEntry.Validator = func(s string) error { return validateName(s, c.work.Profiles, c.sel) }
	c.nameEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		// renameProfile keeps ActiveProfile pointing at this profile if it was
		// the active one, so renaming the active profile does not orphan it.
		renameProfile(c.work, c.sel, s)
		c.list.Refresh()
	}

	c.gatewayEntry = widget.NewEntry()
	c.gatewayEntry.SetPlaceHolder("vpn.example.com")
	c.gatewayEntry.Validator = hostValidator()
	c.gatewayEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].Gateway = s
	}

	c.portEntry = widget.NewEntry()
	c.portEntry.Validator = validatePortString
	c.portEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		if n, err := parsePort(s); err == nil {
			c.work.Profiles[c.sel].Port = n
		} else {
			c.work.Profiles[c.sel].Port = 0 // flagged invalid; Save's validator catches it
		}
	}

	c.customPort = widget.NewCheck("Use custom port", func(on bool) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].CustomPort = on
		c.applyCustomPortState(on)
	})

	c.authSelect = widget.NewSelect(authLabels, func(label string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].Auth.Method = authMethod(label)
		c.updateAuthNote()
	})
	c.authNote = widget.NewLabel("")
	c.authNote.Importance = widget.WarningImportance

	// Auth sub-fields. Only SAML is wired into the runtime; these are shown so
	// the roadmap is visible but kept disabled (updateAuthNote toggles them), and
	// Save refuses to activate a non-SAML profile. They still round-trip to the
	// config so the shape is forward-designed.
	c.usernameEntry = widget.NewEntry()
	c.usernameEntry.SetPlaceHolder("username (password auth — not yet supported)")
	c.usernameEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].Auth.Username = s
	}
	c.certPathEntry = widget.NewEntry()
	c.certPathEntry.SetPlaceHolder("client certificate path (not yet supported)")
	c.certPathEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].Auth.CertPath = s
	}

	c.realmEntry = widget.NewEntry()
	c.realmEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].Realm = s
	}

	c.autoConnect = widget.NewCheck("Auto-connect at login", func(on bool) {
		if c.loading {
			return
		}
		c.work.Autostart = on
		if on {
			c.work.ActiveProfile = c.work.Profiles[c.sel].Name
			c.list.Refresh()
		}
	})

	c.keepAlive = widget.NewCheck("Keep VPN up (auto-reconnect)", func(on bool) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].KeepAlive = on
	})

	form := widget.NewForm(
		widget.NewFormItem("Profile name", c.nameEntry),
		widget.NewFormItem("Gateway host", c.gatewayEntry),
		widget.NewFormItem("", c.customPort),
		widget.NewFormItem("Port", c.portEntry),
		widget.NewFormItem("Authentication", c.authSelect),
		widget.NewFormItem("", c.authNote),
		widget.NewFormItem("Username", c.usernameEntry),
		widget.NewFormItem("Certificate", c.certPathEntry),
		widget.NewFormItem("Realm", c.realmEntry),
		widget.NewFormItem("", c.autoConnect),
		widget.NewFormItem("", c.keepAlive),
	)
	return form
}

func (c *Controller) buildActionStrip() fyne.CanvasObject {
	c.statusText = canvas.NewText("Disconnected", colorFor(statusGray))
	c.statusText.TextStyle = fyne.TextStyle{Bold: true}

	c.connectBtn = widget.NewButton("Connect", c.host.Connect)
	c.disconnectBtn = widget.NewButton("Disconnect", c.host.Disconnect)
	c.disconnectBtn.Disable()

	saveBtn := widget.NewButton("Save", func() { c.save(false) })
	saveBtn.Importance = widget.HighImportance
	reconnectBtn := widget.NewButton("Save & Reconnect", func() { c.save(true) })
	cancelBtn := widget.NewButton("Cancel", c.cancel)

	buttons := container.NewHBox(
		c.connectBtn, c.disconnectBtn,
		widget.NewSeparator(),
		saveBtn, reconnectBtn, cancelBtn,
	)
	// Status on the left, actions on the right.
	return container.NewBorder(widget.NewSeparator(), nil, container.NewPadded(c.statusText), buttons)
}

// buildBanner constructs the persistent inline banner shown at the top of the
// window when Connect is refused: a warning icon, a bold wrapping message, and a
// dismiss button, over a subtle amber background that reads on both light and
// dark themes. It starts hidden; ShowIssue fills and reveals it.
func (c *Controller) buildBanner() {
	c.bannerLabel = widget.NewLabel("")
	c.bannerLabel.Wrapping = fyne.TextWrapWord
	c.bannerLabel.TextStyle = fyne.TextStyle{Bold: true}

	bg := canvas.NewRectangle(color.NRGBA{R: 0xB8, G: 0x86, B: 0x0B, A: 0x33})
	icon := widget.NewIcon(theme.WarningIcon())
	dismiss := widget.NewButtonWithIcon("", theme.CancelIcon(), c.hideBanner)
	dismiss.Importance = widget.LowImportance

	inner := container.NewBorder(nil, nil, icon, dismiss, container.NewPadded(c.bannerLabel))
	c.banner = container.NewStack(bg, container.NewPadded(inner))
	c.banner.Hide()
}

// showBanner fills the banner with msg and reveals it.
func (c *Controller) showBanner(msg string) {
	c.bannerLabel.SetText(msg)
	c.banner.Show()
	c.banner.Refresh()
}

// hideBanner dismisses the banner. Wired to the dismiss button and called after
// a successful Save (the issue it named is resolved).
func (c *Controller) hideBanner() {
	c.banner.Hide()
	c.banner.Refresh()
}

// ShowIssue reveals the settings window and guides the user straight to the
// field blocking Connect. It re-syncs the form to the saved config first —
// Connect dials what is saved, not any unsaved edits, so the guidance must
// match the saved profile — then selects the issue's profile, switches to its
// tab, marks the offending field invalid and focuses it, and raises the
// persistent banner naming the exact fix.
//
// It mutates widgets, so it must run on the UI goroutine. Both Connect entry
// points that reach it (the tray's Connect item and the window's Connect
// button) already run there, so it takes no lock and does no fyne.Do of its own.
func (c *Controller) ShowIssue(issue *Issue) {
	if issue == nil {
		return
	}
	c.reset()
	c.sel = c.indexOf(issue.ProfileName)
	c.list.Select(c.sel) // fires OnSelected → loadProfile paints the form
	c.selectTab(issue.Tab)
	c.markField(issue)
	c.showBanner(issue.Message)
	c.win.Show()
	c.win.RequestFocus()
}

// selectTab switches the Basic/Advanced container to the tab an issue lives on.
func (c *Controller) selectTab(tab string) {
	if c.tabs == nil {
		return
	}
	if tab == TabAdvanced {
		c.tabs.SelectIndex(1)
		return
	}
	c.tabs.SelectIndex(0)
}

// markField puts the issue's field into its validation-error state and focuses
// it. Entries are marked via AlwaysShowValidationError with an explicit error,
// so even the empty-gateway case — which the entry's own validator deliberately
// accepts (an unconfigured profile is savable) — still shows as invalid. The
// auth control is a Select with no error affordance, so it is only focused; the
// banner carries the "choose SAML / SSO" instruction.
func (c *Controller) markField(issue *Issue) {
	var focus fyne.Focusable
	switch issue.Field {
	case FieldGateway:
		markEntryInvalid(c.gatewayEntry, "a gateway host is required")
		focus = c.gatewayEntry
	case FieldPort:
		markEntryInvalid(c.portEntry, "enter a port between 1 and 65535")
		focus = c.portEntry
	case FieldAuth:
		focus = c.authSelect
	case FieldServerCert:
		markEntryInvalid(c.certPin, "a fingerprint is required to pin the certificate")
		focus = c.certPin
	case FieldSplitDNS:
		markEntryInvalid(c.splitDNS, "one domain per line, e.g. corp.example.com")
		focus = c.splitDNS
	}
	if focus != nil {
		c.win.Canvas().Focus(focus)
	}
}

// markEntryInvalid forces an entry into its error state. AlwaysShowValidationError
// makes SetValidationError stick even when the entry validator would accept the
// current text (as the gateway validator accepts empty); the error clears the
// moment the user types a value the validator accepts.
func markEntryInvalid(e *widget.Entry, msg string) {
	e.AlwaysShowValidationError = true
	e.SetValidationError(errors.New(msg))
}

// loadProfile paints the form from work.Profiles[i]. loading is set for the
// duration so the OnChanged handlers do not echo these values back into the
// working copy.
func (c *Controller) loadProfile(i int) {
	if i < 0 || i >= len(c.work.Profiles) {
		return
	}
	c.sel = i
	p := c.work.Profiles[i]
	c.loading = true
	c.nameEntry.SetText(p.Name)
	c.gatewayEntry.SetText(p.Gateway)
	c.customPort.SetChecked(p.CustomPort)
	c.portEntry.SetText(itoa(effectivePort(p.CustomPort, p.Port)))
	c.applyCustomPortState(p.CustomPort)
	c.authSelect.SetSelected(authLabel(p.Auth.Method))
	c.usernameEntry.SetText(p.Auth.Username)
	c.certPathEntry.SetText(p.Auth.CertPath)
	c.realmEntry.SetText(p.Realm)
	c.autoConnect.SetChecked(c.work.Autostart && c.work.ActiveProfile == p.Name)
	c.keepAlive.SetChecked(p.KeepAlive)

	// Advanced tab.
	c.dualStack.SetChecked(p.DualStack)
	c.dtls.SetChecked(p.DTLS)
	c.rememberSession.SetChecked(p.RememberSession)
	c.certMode.SetSelected(certModeLabel(p.ServerCert.Mode))
	c.certPin.SetText(p.ServerCert.Pin)
	c.applyCertMode(p.ServerCert.Mode)
	c.splitDNS.SetText(strings.Join(p.SplitDNS, "\n"))
	c.samlPortEntry.SetText(itoa(effectiveSAMLPort(p.SAMLPort)))
	c.openconnectPath.SetText(effectiveOpenconnectPath(c.work.OpenconnectPath))
	c.helperPath.SetText(c.work.HelperPath)

	c.loading = false
	c.updateAuthNote()
}

// applyCustomPortState enables the Port entry only when a custom port is in use;
// otherwise it shows the fixed default and is disabled.
func (c *Controller) applyCustomPortState(on bool) {
	if on {
		c.portEntry.Enable()
		return
	}
	c.portEntry.SetText(itoa(defaultPort))
	c.portEntry.Disable()
	if !c.loading {
		c.work.Profiles[c.sel].Port = defaultPort
	}
}

// updateAuthNote shows the "(not yet supported)" note for the two methods that
// are designed in the schema but not wired into the runtime yet, and reveals the
// matching sub-field — always disabled, because none of them are functional.
// SAML has no sub-field and clears the note. Save's validateAuthSupported is the
// real gate; this is only the visual affordance.
func (c *Controller) updateAuthNote() {
	method := c.work.Profiles[c.sel].Auth.Method
	// Sub-fields are never editable today: only SAML is implemented.
	c.usernameEntry.Disable()
	c.certPathEntry.Disable()
	switch method {
	case config.AuthPassword:
		c.authNote.SetText("(username/password auth not yet supported — use SAML/SSO)")
		c.usernameEntry.Show()
		c.certPathEntry.Hide()
	case config.AuthCert:
		c.authNote.SetText("(client-certificate auth not yet supported — use SAML/SSO)")
		c.usernameEntry.Hide()
		c.certPathEntry.Show()
	default:
		c.authNote.SetText("")
		c.usernameEntry.Hide()
		c.certPathEntry.Hide()
	}
}

// buildAdvancedTab builds the Advanced form: dual-stack, DTLS, the server-cert
// mode radio (+ conditional fingerprint entry), split-DNS domains, SAML redirect
// port, the machine-wide openconnect path and the read-only helper path.
func (c *Controller) buildAdvancedTab() fyne.CanvasObject {
	c.dualStack = widget.NewCheck("Enable IPv6 / dual-stack", func(on bool) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].DualStack = on
	})
	c.dtls = widget.NewCheck("Prefer DTLS (UDP)", func(on bool) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].DTLS = on
	})
	c.rememberSession = widget.NewCheck("Reuse session to avoid re-login", func(on bool) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].RememberSession = on
	})

	c.certPin = widget.NewEntry()
	c.certPin.SetPlaceHolder("e.g. sha256:AB:CD:...")
	c.certPin.Validator = fingerprintCharset
	c.certPin.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].ServerCert.Pin = s
	}
	c.certPinItem = widget.NewFormItem("Fingerprint", c.certPin)

	c.certMode = widget.NewRadioGroup(certModeLabels, func(label string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].ServerCert.Mode = certMode(label)
		c.applyCertMode(certMode(label))
	})

	c.splitDNS = widget.NewMultiLineEntry()
	c.splitDNS.SetPlaceHolder("one domain per line, e.g.\ncorp.example.com\ninternal")
	c.splitDNS.Validator = validateSplitDNSText
	c.splitDNS.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.Profiles[c.sel].SplitDNS = parseSplitDNS(s)
	}
	// TODO(task11): SplitDNS is only captured + validated here. The scoped
	// /etc/resolver install/remove that makes these domains resolve through the
	// tunnel is a separate task; nothing installs a resolver yet.
	splitDNSNote := widget.NewLabel("Domains routed through the VPN's DNS (scoped resolver — installed in a later release).")
	splitDNSNote.Wrapping = fyne.TextWrapWord

	c.samlPortEntry = widget.NewEntry()
	c.samlPortEntry.Validator = validatePortString
	c.samlPortEntry.OnChanged = func(s string) {
		if c.loading {
			return
		}
		if n, err := parsePort(s); err == nil {
			c.work.Profiles[c.sel].SAMLPort = n
		} else {
			c.work.Profiles[c.sel].SAMLPort = 0 // flagged invalid; Save's validator catches it
		}
	}

	c.openconnectPath = widget.NewEntry()
	c.openconnectPath.Validator = openconnectPathEntryValidator
	c.openconnectPath.OnChanged = func(s string) {
		if c.loading {
			return
		}
		c.work.OpenconnectPath = s
	}
	openconnectNote := widget.NewLabel("Only used on Windows; macOS/Linux dial through the privileged helper.")
	openconnectNote.Wrapping = fyne.TextWrapWord

	// Read-only: the sudoers rule is scoped to exactly this path, so editing it
	// here without re-running install.sh would break sudo. Shown for reference.
	c.helperPath = widget.NewEntry()
	c.helperPath.Disable()
	helperNote := widget.NewLabel("Changing this requires re-running scripts/install.sh.")
	helperNote.Importance = widget.WarningImportance
	helperNote.Wrapping = fyne.TextWrapWord

	rememberNote := widget.NewLabel("Skips the browser login while the session is valid; off never stores it.")
	rememberNote.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("", c.dualStack),
		widget.NewFormItem("", c.dtls),
		widget.NewFormItem("", c.rememberSession),
		widget.NewFormItem("", rememberNote),
		widget.NewFormItem("Server certificate", c.certMode),
		c.certPinItem,
		widget.NewFormItem("Split-DNS domains", c.splitDNS),
		widget.NewFormItem("", splitDNSNote),
		widget.NewFormItem("SAML redirect port", c.samlPortEntry),
		widget.NewFormItem("openconnect binary", c.openconnectPath),
		widget.NewFormItem("", openconnectNote),
		widget.NewFormItem("Privileged helper", c.helperPath),
		widget.NewFormItem("", helperNote),
	)
	return container.NewVScroll(form)
}

// applyCertMode reveals the fingerprint entry only when the Pin mode is chosen.
// Hiding the FormItem keeps the fingerprint out of sight for the other modes;
// its validator tolerates empty so a hidden field never blocks the form.
func (c *Controller) applyCertMode(mode config.ServerCertMode) {
	if mode == config.CertPin {
		c.certPinItem.Widget.Show()
		c.certPin.Enable()
	} else {
		c.certPin.Disable()
		c.certPinItem.Widget.Hide()
	}
}

func (c *Controller) save(reconnect bool) {
	work := cloneConfig(c.work)
	normalizePorts(work)
	if err := validateConfig(work); err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	if err := c.host.Commit(work); err != nil {
		dialog.ShowError(err, c.win)
		return
	}
	// Keep the visible working copy consistent with what was just persisted.
	c.work = cloneConfig(work)
	// The config now validates, so any Connect-issue banner it raised is stale.
	c.hideBanner()
	if reconnect {
		// Reaching a running tunnel with the new settings: tear the current one
		// down and bring it back up so the supervisor re-reads the active profile.
		c.host.Disconnect()
		c.host.Connect()
		return
	}
	dialog.ShowInformation("Saved", "Settings saved.", c.win)
}

func (c *Controller) cancel() {
	c.reset()
	c.win.Hide()
}

func (c *Controller) addProfile() {
	name := uniqueName("New profile", c.work.Profiles)
	c.work.Profiles = append(c.work.Profiles, config.NewProfile(name))
	c.sel = len(c.work.Profiles) - 1
	c.list.Refresh()
	c.list.Select(c.sel)
	c.loadProfile(c.sel)
	c.win.Canvas().Focus(c.nameEntry)
}

func (c *Controller) duplicateProfile() {
	src := c.work.Profiles[c.sel]
	dup := *cloneProfile(&src)
	dup.Name = uniqueName(src.Name+" copy", c.work.Profiles)
	c.work.Profiles = append(c.work.Profiles, dup)
	c.sel = len(c.work.Profiles) - 1
	c.list.Refresh()
	c.list.Select(c.sel)
	c.loadProfile(c.sel)
}

func (c *Controller) deleteProfile() {
	if !canDeleteProfile(len(c.work.Profiles)) {
		dialog.ShowInformation("Cannot delete",
			"This is the last profile; at least one must remain.", c.win)
		return
	}
	victim := c.work.Profiles[c.sel]
	dialog.ShowConfirm("Delete profile",
		"Delete the profile \""+victim.Name+"\"?",
		func(ok bool) {
			if !ok {
				return
			}
			c.work.Profiles = append(c.work.Profiles[:c.sel], c.work.Profiles[c.sel+1:]...)
			// If the active profile was removed, fall back to the first one.
			if c.work.ActiveProfile == victim.Name {
				c.work.ActiveProfile = c.work.Profiles[0].Name
			}
			if c.sel >= len(c.work.Profiles) {
				c.sel = len(c.work.Profiles) - 1
			}
			c.list.Refresh()
			c.list.Select(c.sel)
			c.loadProfile(c.sel)
		}, c.win)
}

func (c *Controller) setActive() {
	c.work.ActiveProfile = c.work.Profiles[c.sel].Name
	c.list.Refresh()
	// The auto-connect checkbox reflects "this profile is active"; refresh it.
	c.autoConnect.SetChecked(c.work.Autostart && c.work.ActiveProfile == c.work.Profiles[c.sel].Name)
}

// cloneProfile deep-copies a single profile (its only reference type is the
// SplitDNS slice), so Duplicate cannot alias the source's slice.
func cloneProfile(p *config.Profile) *config.Profile {
	out := *p
	if p.SplitDNS != nil {
		out.SplitDNS = append([]string(nil), p.SplitDNS...)
	}
	return &out
}

// colorFor maps a status kind to a fixed colour readable on both light and dark
// window backgrounds.
func colorFor(k statusKind) color.Color {
	switch k {
	case statusGreen:
		return color.NRGBA{R: 0x2E, G: 0x7D, B: 0x32, A: 0xFF}
	case statusYellow:
		return color.NRGBA{R: 0xB8, G: 0x86, B: 0x0B, A: 0xFF}
	case statusRed:
		return color.NRGBA{R: 0xC6, G: 0x28, B: 0x28, A: 0xFF}
	default:
		return color.NRGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xFF}
	}
}
