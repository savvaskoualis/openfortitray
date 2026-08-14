package settings

import (
	"errors"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
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

	// forms is every widget.Form the tabs are built from, so a theme or validation
	// change can be pushed through all of them at once.
	forms []*widget.Form

	nameEntry    *widget.Entry
	gatewayEntry *widget.Entry
	portEntry    *widget.Entry
	authSelect   *widget.Select
	authNote     *widget.Label
	// The rows that appear and disappear with the chosen auth method, each in its
	// own container so hiding one reclaims its space as well as its label (see row).
	authNoteRow *fyne.Container
	autoConnect *widget.Check
	keepAlive   *widget.Check

	// Advanced tab.
	dualStack       *widget.Check
	dtls            *widget.Check
	rememberSession *widget.Check
	certMode        *widget.RadioGroup
	certPin         *widget.Entry
	certPinRow      *fyne.Container
	splitDNS        *widget.Entry
	samlPortEntry   *widget.Entry
	openconnectPath *widget.Entry
	helperPath      *widget.Entry

	statusText *canvas.Text
	// reconnectBtn is "Save & Reconnect", enabled only while a tunnel is up.
	reconnectBtn *widget.Button
	// activeBtn promotes the selected profile to the active one.
	activeBtn *widget.Button

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
	// Save & Reconnect only makes sense against a tunnel there is something to
	// bounce. Offered when nothing is up, it is just a slower Save that briefly
	// dials — so it is disabled instead, which says so without a word of UI text.
	if active {
		c.reconnectBtn.Enable()
	} else {
		c.reconnectBtn.Disable()
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

	// Profile rail. The four actions used to be a 2x2 grid of equal text buttons
	// wedged under the list, which read as a debug panel: four same-weight controls,
	// no indication that three are rare and one is routine.
	//
	// Now the three list-editing actions are icon buttons on one row — add,
	// duplicate, delete, in that order of destructiveness — and "Set active", the one
	// that changes what the app actually connects to, gets its own labelled row
	// because it is a different KIND of action from editing the list.
	addBtn := widget.NewButtonWithIcon("", theme.ContentAddIcon(), c.addProfile)
	dupBtn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), c.duplicateProfile)
	delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), c.deleteProfile)
	for _, b := range []*widget.Button{addBtn, dupBtn, delBtn} {
		b.Importance = widget.LowImportance
	}
	c.activeBtn = widget.NewButton("Set as active", c.setActive)
	c.activeBtn.Importance = widget.LowImportance
	railTools := container.NewVBox(
		container.NewGridWithColumns(3, addBtn, dupBtn, delBtn),
		c.activeBtn,
	)
	railCap := canvas.NewText("PROFILES", theme.Color(theme.ColorNamePlaceHolder))
	railCap.TextSize = theme.Size(theme.SizeNameCaptionText)
	railCap.TextStyle = fyne.TextStyle{Bold: true}
	left := container.NewBorder(
		container.NewPadded(railCap), railTools, nil, nil, c.list)

	bottom := c.buildActionStrip()

	// The banner sits at the top; while hidden BorderLayout gives it no space.
	// A rule between the rail and the panel: without it the two columns float in one
	// undifferentiated field of background, which is most of why the window read as
	// unfinished even once the spacing was right.
	content := container.NewBorder(c.banner, bottom,
		container.NewHBox(left, widget.NewSeparator()), nil, c.tabs)
	c.win.SetContent(content)
	c.win.Resize(fyne.NewSize(720, 560))
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

func (c *Controller) buildBasicTab() fyne.CanvasObject {
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
			// CustomPort is no longer a control the user sees — it is derived from
			// whether the port differs from the default. It stays in the schema
			// because FortiClient's EnableCustomPort maps onto it, and because
			// effectivePort still reads it, but asking someone to tick a box before
			// they may type a port was two controls for one value.
			c.work.Profiles[c.sel].CustomPort = n != defaultPort
		} else {
			c.work.Profiles[c.sel].Port = 0 // flagged invalid; Save's validator catches it
			c.work.Profiles[c.sel].CustomPort = true
		}
	}

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

	c.authNoteRow = c.row("", c.authNote)

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

	// Same eleven fields, same order — grouped under captions instead of dumped in
	// one column. The grouping is the whole change: a flat eleven-row form gives a
	// reader no way to tell which fields belong together, and "Realm" next to
	// "Auto-connect at login" implies a relationship that does not exist.
	return sections(
		c.section("Connection",
			widget.NewFormItem("Profile name", c.nameEntry),
			widget.NewFormItem("Gateway host", c.gatewayEntry),
			widget.NewFormItem("Port", narrow(c.portEntry, 150)),
		),
		c.group("Authentication",
			c.row("Method", c.authSelect),
			// The three conditional rows sit outside that form, each in its own
			// container, so the group closes up under SAML instead of leaving a hole.
			c.authNoteRow,
		),
		c.section("Startup",
			widget.NewFormItem("", c.autoConnect),
			widget.NewFormItem("", c.keepAlive),
		),
	)
}

// section is one captioned group of form rows: an uppercase caption in the muted
// foreground, then the rows.
//
// The caption is plain uppercase with no letter-spacing. fyne offers no tracking,
// and the usual hack — inserting spaces between characters — breaks text selection
// and reads the letters out individually to a screen reader, which is a real cost
// for a cosmetic gain.
func (c *Controller) section(caption string, items ...*widget.FormItem) fyne.CanvasObject {
	form := widget.NewForm(items...)
	c.forms = append(c.forms, form)
	return c.group(caption, form)
}

// show reveals or hides one of those rows. A nil row is tolerated so the
// build order does not have to be perfect.
//
// Hiding is only half the job — see (*Controller).relayout.
func show(row *fyne.Container, visible bool) {
	if row == nil {
		return
	}
	if visible {
		row.Show()
	} else {
		row.Hide()
	}
	row.Refresh()
}

// relayout re-lays out the tabs after a row's visibility changed.
//
// Hiding a widget in fyne does NOT relayout its ancestors. A container keeps the
// size it computed while the child was still visible, and its own MinSize quietly
// drops without anything acting on the difference. The rows are hidden during the
// first loadProfile, which runs AFTER build, so the Authentication group was laid
// out at 210px while reporting a minimum of 93 — leaving a 120px hole between
// "Method" and "Realm" that looked exactly like a spacing bug and was not.
//
// Measured, not guessed: the group's laid-out size and MinSize disagreed by the
// combined height of the three hidden rows.
func (c *Controller) relayout() {
	if c.tabs != nil {
		c.tabs.Refresh()
	}
}

// narrow caps a field's width and left-aligns it, for values whose length is known
// and short. A form stretches every field to the full column, so a five-digit port
// got the same width as a hostname — which tells the reader nothing about what
// belongs in it, and makes a settings pane look like a generated form rather than a
// designed one.
// The width must leave room for the validation tick a validated Entry draws
// INSIDE its own box: at 110px the digits and the tick collided.
func narrow(o fyne.CanvasObject, w float32) fyne.CanvasObject {
	return container.NewHBox(
		container.New(layout.NewGridWrapLayout(fyne.NewSize(w, 34)), o),
		layout.NewSpacer(),
	)
}

// group is a captioned stack of arbitrary objects, for the groups that mix a form
// with rows that come and go (see row).
func (c *Controller) group(caption string, objs ...fyne.CanvasObject) fyne.CanvasObject {
	label := canvas.NewText(strings.ToUpper(caption), theme.Color(theme.ColorNamePlaceHolder))
	label.TextSize = theme.Size(theme.SizeNameCaptionText)
	label.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewVBox(append([]fyne.CanvasObject{label}, objs...)...)
}

// row builds a single form row inside its own container, so it can be hidden
// COMPLETELY — label, widget and the vertical space they occupied.
//
// This is the only arrangement that works. Hiding a FormItem's widget leaves its
// label drawn beside empty space; blanking item.Text as well removes the text but
// fyne's form layout still reserves the row's height, so the Authentication group
// kept a three-row hole under SAML — which is every real install. A hidden
// container is skipped by the enclosing VBox entirely, which is what "hidden"
// should have meant in the first place.
func (c *Controller) row(label string, w fyne.CanvasObject) *fyne.Container {
	form := widget.NewForm(widget.NewFormItem(label, w))
	c.forms = append(c.forms, form)
	return container.NewVBox(form)
}

// sections stacks captioned groups with a separator between them, inside a
// scroller so a tab taller than the window stays reachable — the Advanced tab is,
// on a small display.
func sections(groups ...fyne.CanvasObject) fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, len(groups)*2)
	for i, g := range groups {
		if i > 0 {
			objs = append(objs, widget.NewSeparator())
		}
		objs = append(objs, g)
	}
	return container.NewVScroll(container.NewPadded(container.NewVBox(objs...)))
}

// buildActionStrip builds the footer: connection state on the left, the two save
// actions and Cancel on the right.
//
// Connect and Disconnect USED to live here, making five equal-weight buttons in one
// row with no hierarchy at all. They are gone: driving the tunnel is the tray's job
// and the status window's job, and a settings window's job is settings. Every
// surface having every control is what "nothing has a meaningful place" looks like.
// The state text stays, because knowing whether a change will disrupt a live tunnel
// is genuinely settings context.
func (c *Controller) buildActionStrip() fyne.CanvasObject {
	c.statusText = canvas.NewText("Disconnected", colorFor(statusGray))
	c.statusText.TextSize = theme.Size(theme.SizeNameCaptionText)
	c.statusText.TextStyle = fyne.TextStyle{Bold: true}

	// One high-importance action. Save & Reconnect is the same commit plus a tunnel
	// bounce, so it reads as a variant of Save rather than a rival to it; Cancel is
	// quietest because discarding is never what someone came here to do.
	saveBtn := widget.NewButton("Save", func() { c.save(false) })
	saveBtn.Importance = widget.HighImportance
	c.reconnectBtn = widget.NewButton("Save & Reconnect", func() { c.save(true) })
	c.reconnectBtn.Importance = widget.MediumImportance
	cancelBtn := widget.NewButton("Cancel", c.cancel)
	cancelBtn.Importance = widget.LowImportance

	buttons := container.NewHBox(cancelBtn, c.reconnectBtn, saveBtn)
	return container.NewBorder(widget.NewSeparator(), nil,
		container.NewPadded(c.statusText), container.NewPadded(buttons))
}

// buildBanner constructs the persistent inline banner shown at the top of the
// window when Connect is refused: a warning icon, a bold wrapping message, and a
// dismiss button, over a subtle amber background that reads on both light and
// dark themes. It starts hidden; ShowIssue fills and reveals it.
func (c *Controller) buildBanner() {
	c.bannerLabel = widget.NewLabel("")
	c.bannerLabel.Wrapping = fyne.TextWrapWord
	c.bannerLabel.TextStyle = fyne.TextStyle{Bold: true}

	// The banner ground is the theme's warning colour at low alpha, so it tracks the
	// OS light/dark setting instead of being an amber mixed for one of them.
	bg := canvas.NewRectangle(withAlpha(theme.Color(theme.ColorNameWarning), 0x33))
	icon := widget.NewIcon(theme.WarningIcon())
	dismiss := widget.NewButtonWithIcon("", theme.CancelIcon(), c.hideBanner)
	dismiss.Importance = widget.LowImportance

	inner := container.NewBorder(nil, nil, icon, dismiss, container.NewPadded(c.bannerLabel))
	c.banner = container.NewStack(bg, container.NewPadded(inner))
	c.banner.Hide()
}

// withAlpha returns c at the given alpha, for the translucent washes (the banner
// ground) that must still follow the theme's hue.
func withAlpha(c color.Color, a uint8) color.Color {
	r, g, b, _ := c.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: a}
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
	c.portEntry.SetText(itoa(effectivePort(p.CustomPort, p.Port)))
	c.authSelect.SetSelected(authLabel(p.Auth.Method))
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

// updateAuthNote shows the "(not yet supported)" note for the two methods that
// are designed in the schema but not wired into the runtime yet, and reveals the
// matching sub-field — always disabled, because none of them are functional.
// SAML has no sub-field and clears the note. Save's validateAuthSupported is the
// real gate; this is only the visual affordance.
func (c *Controller) updateAuthNote() {
	method := c.work.Profiles[c.sel].Auth.Method
	switch method {
	case config.AuthPassword:
		c.authNote.SetText("(username/password auth not yet supported — use SAML/SSO)")
		show(c.authNoteRow, true)
	case config.AuthCert:
		c.authNote.SetText("(client-certificate auth not yet supported — use SAML/SSO)")
		show(c.authNoteRow, true)
	default:
		// SAML: no sub-field and no note, so the note row goes too rather than
		// leaving an empty gap under the Method select.
		c.authNote.SetText("")
		show(c.authNoteRow, false)
	}
	c.relayout()
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
	c.certPinRow = c.row("Fingerprint", c.certPin)

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
	// The old note said this was "installed in a later release". It is not: the
	// tunnel installs scoped resolvers through the privileged helper (see
	// tunnel.splitDNSEnabled). What IS true is that the helper path is macOS and
	// Linux only, so on Windows these domains are stored and not applied — which is
	// the thing a user actually needs told.
	splitDNSNote := widget.NewLabel("Looked up through the VPN's DNS. macOS and Linux only; stored but not applied on Windows.")
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

	// Same rows, same order, grouped. The Paths group last on purpose: it is where
	// the two fields live that break the app if they are wrong, so it should not be
	// the first thing a browsing user reaches for.
	return sections(
		c.section("Tunnel",
			widget.NewFormItem("", c.dualStack),
			widget.NewFormItem("", c.dtls),
		),
		c.section("Session",
			widget.NewFormItem("", c.rememberSession),
			widget.NewFormItem("", rememberNote),
		),
		c.group("Server certificate",
			c.row("Verification", c.certMode),
			// Its own container, so the group closes up when the certificate is not
			// pinned rather than leaving a reserved empty row.
			c.certPinRow,
		),
		c.section("DNS",
			widget.NewFormItem("Split-DNS domains", c.splitDNS),
			widget.NewFormItem("", splitDNSNote),
		),
		c.section("Paths",
			widget.NewFormItem("SAML redirect port", narrow(c.samlPortEntry, 150)),
			widget.NewFormItem("openconnect binary", c.openconnectPath),
			widget.NewFormItem("", openconnectNote),
			widget.NewFormItem("Privileged helper", c.helperPath),
			widget.NewFormItem("", helperNote),
		),
	)
}

// applyCertMode reveals the fingerprint entry only when the Pin mode is chosen.
// Hiding the FormItem keeps the fingerprint out of sight for the other modes;
// its validator tolerates empty so a hidden field never blocks the form.
func (c *Controller) applyCertMode(mode config.ServerCertMode) {
	if mode == config.CertPin {
		show(c.certPinRow, true)
		c.certPin.Enable()
	} else {
		c.certPin.Disable()
		show(c.certPinRow, false)
	}
	c.relayout()
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
// colorFor is the status strip's colour, taken from the theme's semantic tokens
// rather than from literals as it used to be. Hardcoded hex could not follow the
// OS light/dark setting — the old amber and red were mixed for a light background
// and were the wrong contrast on a dark one — and it meant the status strip and
// the status window's dot could disagree about what "connected" looks like.
func colorFor(k statusKind) color.Color {
	switch k {
	case statusGreen:
		return theme.Color(theme.ColorNameSuccess)
	case statusYellow:
		return theme.Color(theme.ColorNameWarning)
	case statusRed:
		return theme.Color(theme.ColorNameError)
	default:
		return theme.Color(theme.ColorNameDisabled)
	}
}
