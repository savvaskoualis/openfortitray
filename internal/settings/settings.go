// Package settings renders the native settings window (profile bar +
// Connection tab + Advanced tab + action strip) on Qt6/miqt and holds the pure
// validation / working-copy logic that drives it.
//
// The window itself cannot be exercised without a display, so every decision
// it makes — field validation, unique/non-empty names, refuse-delete-last, the
// custom-port-off rule, and cloning the config into an editable working copy —
// lives in logic.go as a pure function and is table-tested in logic_test.go.
// This file (settings.go) is a thin shell that wires Qt widgets to those pure
// functions; it owns every widget and holds no validation rule of its own.
package settings

import (
	"errors"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/credstore"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// Host is what the settings window needs from the application. Every method
// is called on the Qt UI thread (from a widget signal handler or from Apply,
// which the event pump marshals there).
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

// errorBorderColor outlines an invalid QLineEdit/QPlainTextEdit. uitheme
// (Task 3) has no exported raw color for its "error" QSS role today — only
// the QLabel[role="error"] selector used for the error labels' text color
// below — so this is a standalone value rather than a uitheme export. It
// reads clearly as an error on both the light and dark palettes.
const errorBorderColor = "#E5484D"

// styler is satisfied by any widget exposing SetStyleSheet — both QLineEdit
// and QPlainTextEdit promote it from their embedded QWidget — so markInvalid
// works uniformly across every validated row in the field-mapping table
// instead of needing one copy per widget type.
type styler interface {
	SetStyleSheet(string)
}

// markInvalid is the one shared error-display helper every validated row
// uses: an outlined border on the field plus a small message label directly
// beneath it (see validatedRow), replacing Fyne's
// SetValidationError/AlwaysShowValidationError.
func markInvalid(w styler, errLabel *qt.QLabel, err error) {
	if err != nil {
		w.SetStyleSheet("border: 1px solid " + errorBorderColor + ";")
		errLabel.SetText(err.Error())
		errLabel.SetVisible(true)
	} else {
		w.SetStyleSheet("")
		errLabel.SetVisible(false)
	}
}

// fieldTarget is what ShowIssue needs to guide the user to one field: an
// optional mark function (nil for the Select/Button fields that have no error
// affordance of their own — the banner names the fix instead, exactly as the
// Fyne version did) and a focus function.
type fieldTarget struct {
	mark  func(error)
	focus func()
}

// Controller owns the settings content and every widget in it. It is built
// once at startup (New) and reused: the composed panes are handed to the
// shell, which decides where and when they are shown. Apply feeds the live
// status strip from the one event pump. All methods run on the UI thread.
type Controller struct {
	host Host
	win  *qt.QMainWindow

	// work is the in-memory copy being edited; nothing reaches the live
	// config until Save/Commit. sel is the index into work.Profiles shown in
	// the form.
	work *config.Config
	sel  int

	// profileSelect replaces the old profile rail; syncing suppresses its
	// OnCurrentIndexChanged while syncProfileBar repaints it.
	profileSelect *qt.QComboBox
	profileBar    *qt.QWidget
	syncing       bool

	// The composed panes the shell arranges. This controller does not own a
	// window: it owns widgets, and something else decides where they live.
	basic    *qt.QWidget
	advanced *qt.QWidget
	footer   *qt.QWidget
	navigate func(tab string)

	// connectionLayout/advancedLayout are the outer QVBoxLayouts of the two
	// tabs, kept so a conditional row's visibility change can force a
	// relayout (see relayout).
	connectionLayout *qt.QVBoxLayout
	advancedLayout   *qt.QVBoxLayout

	// banner is the persistent inline strip shown at the top of the settings
	// sections that names a blocking Connect issue and its fix; it is hidden
	// until ShowIssue raises it and stays up until dismissed or a successful
	// Save.
	banner      *qt.QWidget
	bannerLabel *qt.QLabel

	nameEntry       *qt.QLineEdit
	nameErrLabel    *qt.QLabel
	gatewayEntry    *qt.QLineEdit
	gatewayErrLabel *qt.QLabel
	portEntry       *qt.QLineEdit
	portErrLabel    *qt.QLabel
	authSelect      *qt.QComboBox
	authNote        *qt.QLabel
	// authNoteRow is its own container, shown/hidden as a whole (see
	// updateAuthNote), so hiding it reclaims its space entirely.
	authNoteRow   *qt.QWidget
	backendSelect *qt.QComboBox
	autoConnect   *qt.QCheckBox
	keepAlive     *qt.QCheckBox

	// IPsec auth fields (Connection tab). ipsecSecretDirty/ipsecSecretValue
	// track the PSK entry, keyed by profile index (c.sel) — mirroring how
	// every other field writes straight into c.work.Profiles[c.sel] — so an
	// unsaved PSK typed for one profile survives switching the profile
	// dropdown to another and back, exactly like every other in-memory edit,
	// instead of silently evaporating. deleteProfile reindexes both maps when
	// a profile is removed. The value never round-trips from a stored
	// credstore secret back into the widget — the maps only ever hold what
	// was typed this session (loadProfile reads them, never credstore); an
	// unset index is the zero value (false / ""), which is exactly "nothing
	// typed yet for this profile".
	ipsecAuthSelect     *qt.QComboBox
	ipsecSecretEntry    *qt.QLineEdit
	ipsecSecretErrLabel *qt.QLabel
	ipsecSecretDirty    map[int]bool
	ipsecSecretValue    map[int]string
	ipsecCertPathEntry  *qt.QLineEdit
	ipsecKeyPathEntry   *qt.QLineEdit
	ipsecCertPathButton *qt.QPushButton
	ipsecKeyPathButton  *qt.QPushButton
	ipsecPSKRow         *qt.QWidget
	ipsecCertRow        *qt.QWidget
	ipsecKeyRow         *qt.QWidget

	// Advanced tab.
	dualStack               *qt.QCheckBox
	dtls                    *qt.QCheckBox
	rememberSession         *qt.QCheckBox
	certMode                *qt.QComboBox
	certPin                 *qt.QLineEdit
	certPinErrLabel         *qt.QLabel
	certPinRow              *qt.QWidget
	splitDNS                *qt.QPlainTextEdit
	splitDNSErrLabel        *qt.QLabel
	samlPortEntry           *qt.QLineEdit
	samlPortErrLabel        *qt.QLabel
	openconnectPath         *qt.QLineEdit
	openconnectPathErrLabel *qt.QLabel
	helperPath              *qt.QLabel

	// IPsec proposal/identity fields (Advanced tab). Always visible regardless
	// of Backend, like every other forward-designed-but-inactive field in
	// this tab — inert for an SSL profile.
	ikeProposalEntry *qt.QLineEdit
	espProposalEntry *qt.QLineEdit
	localIDEntry     *qt.QLineEdit
	remoteIDEntry    *qt.QLineEdit

	statusText *qt.QLabel
	// reconnectBtn is "Save & Reconnect", enabled only while a tunnel is up.
	reconnectBtn *qt.QPushButton
	// delBtn is disabled when only one profile remains, which states the
	// rule without a dialog. savedNote is the transient "Saved" confirmation.
	delBtn    *qt.QPushButton
	savedNote *qt.QLabel
	// activeBtn promotes the selected profile to the active one.
	activeBtn *qt.QPushButton

	// loading suppresses the widgets' change handlers while loadProfile
	// populates them, so repainting the form for a newly selected profile
	// does not write those values straight back into the working copy.
	loading bool

	// fieldTargets maps a Field* constant to the widget(s) ShowIssue must
	// mark invalid and focus. Built once in build(), after every widget it
	// references exists.
	fieldTargets map[string]fieldTarget
}

// New builds the settings content wired to host, placed on win (used only as
// the parent for modal dialogs — the shell owns showing/hiding win itself).
func New(host Host, win *qt.QMainWindow) *Controller {
	c := &Controller{
		host:             host,
		win:              win,
		ipsecSecretDirty: map[int]bool{},
		ipsecSecretValue: map[int]string{},
	}
	// Populate the working copy before build: loadProfile (called from
	// reset(), right after build()) reads c.work and c.sel.
	c.work = cloneConfig(host.Config())
	c.sel = c.indexOf(c.work.ActiveProfile)
	c.build()
	c.reset()
	return c
}

// Show refreshes the working copy from the live config (discarding any edits
// left from a previous session). Revealing the window is the shell's job.
func (c *Controller) Show() {
	c.reset()
}

// Apply renders one tunnel event onto the bottom status strip and mirrors the
// tray's Connect/Disconnect enabling. It is called only from the shared event
// pump, so it is already on the UI thread and mutates widgets directly.
func (c *Controller) Apply(e tunnel.Event) {
	text, kind, active := statusFor(e)
	c.statusText.SetText(text)
	setRole(c.statusText, roleForStatus(kind))
	// Save & Reconnect only makes sense against a tunnel there is something to
	// bounce. Offered when nothing is up, it is just a slower Save that
	// briefly dials — so it is disabled instead, which says so without a word
	// of UI text.
	c.reconnectBtn.SetEnabled(active)
}

// roleForStatus maps a statusKind onto uitheme's QSS roles (see
// internal/uitheme's [role="success"|"warning"|"error"] selectors).
func roleForStatus(k statusKind) string {
	switch k {
	case statusGreen:
		return "success"
	case statusYellow:
		return "warning"
	case statusRed:
		return "error"
	default:
		return ""
	}
}

// reset discards the working copy and rebuilds it from the live config, then
// repaints the profile bar and the form. Used on Show and on Cancel.
func (c *Controller) reset() {
	c.work = cloneConfig(c.host.Config())
	c.sel = c.indexOf(c.work.ActiveProfile)
	// ipsecSecretDirty/ipsecSecretValue hold in-memory-only edits (never
	// committed until Save's credstore.Set), same as every field in c.work —
	// discarding "any edits left from a previous session" means dropping
	// these too, for every profile, not just the one about to be shown.
	c.ipsecSecretDirty = map[int]bool{}
	c.ipsecSecretValue = map[int]string{}
	c.syncProfileBar()
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

// activeProfile returns the profile currently shown in the form, or nil when
// c.sel is out of range (an empty working copy — defensive, since Add/Delete
// keep at least one profile in normal use).
func (c *Controller) activeProfile() *config.Profile {
	if c.sel < 0 || c.sel >= len(c.work.Profiles) {
		return nil
	}
	return &c.work.Profiles[c.sel]
}

func (c *Controller) build() {
	c.buildProfileBar()
	c.basic = c.buildConnectionTab()
	c.advanced = c.buildAdvancedTab()
	c.buildBanner()
	c.footer = c.buildActionStrip()
	c.buildFieldTargets()
}

// Banner returns the inline warning strip, shown at the top of the settings
// sections. Hidden until ShowIssue raises it.
func (c *Controller) Banner() *qt.QWidget { return c.banner }

// ProfileBar returns the profile selector and its management buttons. It
// governs both settings sections, so the shell places it above whichever is
// showing.
func (c *Controller) ProfileBar() *qt.QWidget { return c.profileBar }

// ConnectionContent and AdvancedContent are the two settings sections,
// presented by the shell as destinations alongside Status.
func (c *Controller) ConnectionContent() *qt.QWidget { return c.basic }
func (c *Controller) AdvancedContent() *qt.QWidget   { return c.advanced }

// Footer returns the Cancel / Save & Reconnect / Save strip.
func (c *Controller) Footer() *qt.QWidget { return c.footer }

// SetNavigator gives the controller a way to ask the shell to show a section,
// so ShowIssue can put the user in front of the offending field.
func (c *Controller) SetNavigator(nav func(tab string)) { c.navigate = nav }

// setRole sets (or clears, with role == "") the "role" dynamic property that
// uitheme's stylesheet keys its [role="..."] selectors on, then forces Qt to
// re-evaluate the stylesheet against this widget. Qt does NOT automatically
// repolish a widget when an arbitrary dynamic property changes — only the
// unpolish/polish pair below makes an attribute-selector style update take
// effect after the widget has already been shown once (see internal/status's
// identical helper).
func setRole(w *qt.QLabel, role string) {
	w.SetProperty("role", qt.NewQVariant11(role))
	if s := w.Style(); s != nil {
		s.Unpolish(w.QWidget)
		s.Polish(w.QWidget)
	}
}

// newCaption is an uppercase, muted section heading, reusing uitheme's
// "caption" QSS role.
func newCaption(text string) *qt.QLabel {
	l := qt.NewQLabel3(strings.ToUpper(text))
	setRole(l, "sectionHeader")
	return l
}

// newNote is a wrapping, muted line of explanatory text under a field.
func newNote(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	l.SetWordWrap(true)
	setRole(l, "caption")
	return l
}

// newSeparator is a thin rule between groups, reusing uitheme's "separator"
// QSS role (the same background token the tray/status windows use).
func newSeparator() *qt.QLabel {
	l := qt.NewQLabel2()
	setRole(l, "separator")
	l.SetFixedHeight(1)
	return l
}

func addCaption(layout *qt.QVBoxLayout, text string) { layout.AddWidget(newCaption(text).QWidget) }
func addSeparator(layout *qt.QVBoxLayout)            { layout.AddWidget(newSeparator().QWidget) }

// newScrollableColumn builds a tab's outer frame: a padded, vertically
// stacked column of rows and groups inside a scroll area, so a tab taller
// than the window stays reachable — the Advanced tab is, on a small display.
func newScrollableColumn() (*qt.QScrollArea, *qt.QVBoxLayout) {
	inner := qt.NewQWidget(nil)
	layout := qt.NewQVBoxLayout2()
	layout.SetContentsMargins(16, 16, 16, 16)
	layout.SetSpacing(8)
	inner.SetLayout(layout.QLayout)

	scroll := qt.NewQScrollArea2()
	scroll.SetWidget(inner)
	scroll.SetWidgetResizable(true)
	return scroll, layout
}

// wrapWithError places a QFormLayout row and a hidden error QLabel beneath it
// inside the row's own QVBoxLayout — the shape every validated row in the
// field-mapping table needs, so markInvalid's show/hide has somewhere to
// paint and hiding the WHOLE row (not just the field) is possible elsewhere.
func wrapWithError(form *qt.QFormLayout) (*qt.QLabel, *qt.QWidget) {
	errLabel := qt.NewQLabel2()
	setRole(errLabel, "error")
	errLabel.SetWordWrap(true)
	errLabel.SetVisible(false)

	box := qt.NewQVBoxLayout2()
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(2)
	box.AddLayout(form.QLayout)
	box.AddWidget(errLabel.QWidget)

	row := qt.NewQWidget(nil)
	row.SetLayout(box.QLayout)
	return errLabel, row
}

// validatedRow builds one QLineEdit field row with a hidden error label
// beneath it. width, when > 0, caps the field's pixel width for a value whose
// length is known and short (a port number), so it does not stretch to the
// full column.
func validatedRow(labelText string, width int) (*qt.QLineEdit, *qt.QLabel, *qt.QWidget) {
	entry := qt.NewQLineEdit2()
	if width > 0 {
		entry.SetFixedWidth(width)
	}
	form := qt.NewQFormLayout2()
	form.SetContentsMargins(0, 0, 0, 0)
	form.AddRow3(labelText, entry.QWidget)
	errLabel, row := wrapWithError(form)
	return entry, errLabel, row
}

// plainRow builds one QLineEdit field row with no validator and no error
// label — the IPsec proposal/identity fields, which are inert for an SSL
// profile and carry no validation.
func plainRow(labelText string) (*qt.QLineEdit, *qt.QWidget) {
	entry := qt.NewQLineEdit2()
	form := qt.NewQFormLayout2()
	form.SetContentsMargins(0, 0, 0, 0)
	form.AddRow3(labelText, entry.QWidget)
	row := qt.NewQWidget(nil)
	row.SetLayout(form.QLayout)
	return entry, row
}

// comboRow builds one QComboBox field row populated with items, in display
// order.
func comboRow(labelText string, items []string) (*qt.QComboBox, *qt.QWidget) {
	box := qt.NewQComboBox2()
	box.AddItems(items)
	form := qt.NewQFormLayout2()
	form.SetContentsMargins(0, 0, 0, 0)
	form.AddRow3(labelText, box.QWidget)
	row := qt.NewQWidget(nil)
	row.SetLayout(form.QLayout)
	return box, row
}

// fileRow builds a read-only path QLineEdit plus a browse QPushButton in one
// QHBoxLayout, wrapped in the row's own container so IPsec-auth visibility
// can hide/show it as a whole (see updateIPsecAuthVisibility). The button
// opens a QFileDialog restricted to an existing file; onChosen is called with
// the picked path.
func fileRow(win *qt.QMainWindow, labelText, buttonText string, onChosen func(path string)) (*qt.QLineEdit, *qt.QPushButton, *qt.QWidget) {
	pathEntry := qt.NewQLineEdit2()
	pathEntry.SetReadOnly(true)

	btn := qt.NewQPushButton3(buttonText)
	btn.OnClicked(func() {
		dlg := qt.NewQFileDialog4(win.QWidget, buttonText)
		dlg.SetFileMode(qt.QFileDialog__ExistingFile)
		if dlg.Exec() == int(qt.QDialog__Accepted) {
			files := dlg.SelectedFiles()
			if len(files) > 0 {
				pathEntry.SetText(files[0])
				onChosen(files[0])
			}
		}
	})

	hbox := qt.NewQHBoxLayout2()
	hbox.SetContentsMargins(0, 0, 0, 0)
	hbox.AddWidget(pathEntry.QWidget)
	hbox.AddWidget(btn.QWidget)

	form := qt.NewQFormLayout2()
	form.SetContentsMargins(0, 0, 0, 0)
	form.AddRow4(labelText, hbox.QLayout)

	row := qt.NewQWidget(nil)
	row.SetLayout(form.QLayout)
	return pathEntry, btn, row
}

func (c *Controller) buildProfileBar() {
	// The profile list used to be a permanent left rail. The left edge is now
	// navigation, and for the handful of profiles anyone actually keeps a
	// rail was always a lot of furniture for a one-of-N choice — a selector
	// says the same thing in one row and puts it beside the fields it
	// governs.
	c.profileSelect = qt.NewQComboBox2()
	c.profileSelect.OnCurrentIndexChanged(func(index int) {
		if c.syncing {
			return
		}
		c.sel = index
		c.loadProfile(c.sel)
	})

	addBtn := qt.NewQPushButton3("Add")
	dupBtn := qt.NewQPushButton3("Duplicate")
	c.delBtn = qt.NewQPushButton3("Delete")
	c.activeBtn = qt.NewQPushButton3("Set as active")

	addBtn.OnClicked(c.addProfile)
	dupBtn.OnClicked(c.duplicateProfile)
	c.delBtn.OnClicked(c.deleteProfile)
	c.activeBtn.OnClicked(c.setActive)

	label := newCaption("Profile")

	btnRow := qt.NewQHBoxLayout2()
	btnRow.SetContentsMargins(0, 0, 0, 0)
	btnRow.AddWidget(addBtn.QWidget)
	btnRow.AddWidget(dupBtn.QWidget)
	btnRow.AddWidget(c.delBtn.QWidget)
	btnRow.AddWidget(c.activeBtn.QWidget)

	top := qt.NewQHBoxLayout2()
	top.SetContentsMargins(0, 0, 0, 0)
	top.AddWidget(label.QWidget)
	top.AddWidget(c.profileSelect.QWidget)
	top.AddLayout(btnRow.QLayout)

	rootLayout := qt.NewQVBoxLayout2()
	rootLayout.SetContentsMargins(12, 8, 12, 8)
	rootLayout.AddLayout(top.QLayout)
	rootLayout.AddWidget(newSeparator().QWidget)

	root := qt.NewQWidget(nil)
	root.SetLayout(rootLayout.QLayout)
	c.profileBar = root
}

// syncProfileBar repaints the selector from the working copy. syncing
// suppresses the combo box's own OnCurrentIndexChanged for the duration:
// Clear/AddItems/SetCurrentIndex all fire it, and without the guard choosing
// a profile would reload it twice — once from the click and once from this
// repaint.
func (c *Controller) syncProfileBar() {
	names := make([]string, len(c.work.Profiles))
	for i, p := range c.work.Profiles {
		name := p.Name
		if name == c.work.ActiveProfile {
			name = "● " + name // the profile the app actually connects with
		}
		names[i] = name
	}
	c.syncing = true
	c.profileSelect.Clear()
	c.profileSelect.AddItems(names)
	if c.sel >= 0 && c.sel < len(names) {
		c.profileSelect.SetCurrentIndex(c.sel)
	}
	c.syncing = false

	// The last profile cannot be deleted. A disabled button states the rule
	// before the click, and needs no words.
	if c.delBtn != nil {
		c.delBtn.SetEnabled(canDeleteProfile(len(c.work.Profiles)))
	}
}

// buildConnectionTab builds the Connection tab's rows, in the same visual
// order as the pre-migration Fyne version: Connection group (name, gateway,
// port, protocol, IPsec auth, then the conditional PSK/cert/key rows),
// Authentication group (method, then the conditional note), Startup group
// (the two checkboxes).
func (c *Controller) buildConnectionTab() *qt.QWidget {
	scroll, layout := newScrollableColumn()

	addCaption(layout, "Connection")

	var row *qt.QWidget

	c.nameEntry, c.nameErrLabel, row = validatedRow("Profile name", 0)
	c.nameEntry.OnTextChanged(func(s string) {
		if c.loading {
			return
		}
		markInvalid(c.nameEntry, c.nameErrLabel, validateName(s, c.work.Profiles, c.sel))
	})
	// The rename itself (and the uniqueness it may collide with) commits on
	// blur or Save, not on every keystroke — see save()'s pre-commit rename,
	// which covers Save without a preceding blur.
	c.nameEntry.OnEditingFinished(func() {
		if c.loading || c.sel < 0 || c.sel >= len(c.work.Profiles) {
			return
		}
		renameProfile(c.work, c.sel, c.nameEntry.Text())
		c.syncProfileBar()
	})
	layout.AddWidget(row)

	c.gatewayEntry, c.gatewayErrLabel, row = validatedRow("Gateway host", 0)
	c.gatewayEntry.SetPlaceholderText("vpn.example.com")
	c.gatewayEntry.OnTextChanged(func(s string) {
		markInvalid(c.gatewayEntry, c.gatewayErrLabel, validateHost(s))
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.Gateway = s
		}
	})
	layout.AddWidget(row)

	c.portEntry, c.portErrLabel, row = validatedRow("Port", 150)
	c.portEntry.OnTextChanged(func(s string) {
		err := validatePortString(s)
		markInvalid(c.portEntry, c.portErrLabel, err)
		if c.loading {
			return
		}
		p := c.activeProfile()
		if p == nil {
			return
		}
		if n, perr := parsePort(s); perr == nil {
			p.Port = n
			// CustomPort is no longer a control the user sees — it is
			// derived from whether the port differs from the default.
			p.CustomPort = n != defaultPort
		} else {
			p.Port = 0 // flagged invalid; Save's validator catches it
			p.CustomPort = true
		}
	})
	layout.AddWidget(row)

	c.backendSelect, row = comboRow("Protocol", backendLabels)
	c.backendSelect.OnCurrentIndexChanged(func(int) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.Backend = backendFromLabel(c.backendSelect.CurrentText())
		}
		c.updateIPsecAuthVisibility()
	})
	layout.AddWidget(row)

	c.ipsecAuthSelect, row = comboRow("IPsec auth", ipsecAuthLabels)
	c.ipsecAuthSelect.OnCurrentIndexChanged(func(int) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.IPsec.AuthMethod = ipsecAuthFromLabel(c.ipsecAuthSelect.CurrentText())
		}
		c.updateIPsecAuthVisibility()
	})
	layout.AddWidget(row)

	// These rows sit below the group's form, each in its own container, so
	// the group closes up under whichever IPsec auth fields do not apply
	// instead of leaving a hole under IPsec auth.
	c.ipsecSecretEntry, c.ipsecSecretErrLabel, c.ipsecPSKRow = validatedRow("Pre-shared key", 0)
	c.ipsecSecretEntry.SetEchoMode(qt.QLineEdit__Password)
	c.ipsecSecretEntry.OnTextChanged(func(v string) {
		if c.loading {
			return
		}
		c.ipsecSecretDirty[c.sel] = true
		c.ipsecSecretValue[c.sel] = v
	})
	layout.AddWidget(c.ipsecPSKRow)

	c.ipsecCertPathEntry, c.ipsecCertPathButton, c.ipsecCertRow = fileRow(c.win, "Certificate", "Choose certificate…", func(path string) {
		if p := c.activeProfile(); p != nil {
			p.IPsec.CertPath = path
		}
	})
	layout.AddWidget(c.ipsecCertRow)

	c.ipsecKeyPathEntry, c.ipsecKeyPathButton, c.ipsecKeyRow = fileRow(c.win, "Private key", "Choose private key…", func(path string) {
		if p := c.activeProfile(); p != nil {
			p.IPsec.KeyPath = path
		}
	})
	layout.AddWidget(c.ipsecKeyRow)

	addSeparator(layout)
	addCaption(layout, "Authentication")

	// Auth sub-field. Only SAML is wired into the runtime; this is shown so
	// the roadmap is visible but kept as a note (updateAuthNote toggles it),
	// and Save refuses to activate an unsupported auth method. It still
	// round-trips to the config so the shape is forward-designed.
	c.authSelect, row = comboRow("Method", authLabels)
	c.authSelect.OnCurrentIndexChanged(func(int) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.Auth.Method = authMethod(c.authSelect.CurrentText())
		}
		c.updateAuthNote()
	})
	layout.AddWidget(row)

	c.authNote = qt.NewQLabel2()
	c.authNote.SetWordWrap(true)
	setRole(c.authNote, "warning")
	noteLayout := qt.NewQVBoxLayout2()
	noteLayout.SetContentsMargins(0, 0, 0, 0)
	noteLayout.AddWidget(c.authNote.QWidget)
	c.authNoteRow = qt.NewQWidget(nil)
	c.authNoteRow.SetLayout(noteLayout.QLayout)
	layout.AddWidget(c.authNoteRow)

	addSeparator(layout)
	addCaption(layout, "Startup")

	c.autoConnect = qt.NewQCheckBox3("Auto-connect at login")
	c.autoConnect.OnToggled(func(checked bool) {
		if c.loading {
			return
		}
		c.work.Autostart = checked
		if checked {
			if p := c.activeProfile(); p != nil {
				c.work.ActiveProfile = p.Name
				c.syncProfileBar()
			}
		}
	})
	layout.AddWidget(c.autoConnect.QWidget)

	c.keepAlive = qt.NewQCheckBox3("Keep VPN up (auto-reconnect)")
	c.keepAlive.OnToggled(func(checked bool) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.KeepAlive = checked
		}
	})
	layout.AddWidget(c.keepAlive.QWidget)

	layout.AddStretch()
	c.connectionLayout = layout
	return scroll.QWidget
}

// buildAdvancedTab builds the Advanced tab: dual-stack, DTLS, session reuse,
// the server-cert mode combo (+ conditional fingerprint field), split-DNS
// domains, the IPsec proposal/identity fields, the SAML redirect port, the
// machine-wide openconnect path and the read-only helper path — in the same
// order as the pre-migration Fyne version.
func (c *Controller) buildAdvancedTab() *qt.QWidget {
	scroll, layout := newScrollableColumn()

	var row *qt.QWidget

	addCaption(layout, "Tunnel")
	c.dualStack = qt.NewQCheckBox3("Enable IPv6 / dual-stack")
	c.dualStack.OnToggled(func(checked bool) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.DualStack = checked
		}
	})
	layout.AddWidget(c.dualStack.QWidget)

	c.dtls = qt.NewQCheckBox3("Prefer DTLS (UDP)")
	c.dtls.OnToggled(func(checked bool) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.DTLS = checked
		}
	})
	layout.AddWidget(c.dtls.QWidget)

	addSeparator(layout)
	addCaption(layout, "Session")
	c.rememberSession = qt.NewQCheckBox3("Reuse session to avoid re-login")
	c.rememberSession.OnToggled(func(checked bool) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.RememberSession = checked
		}
	})
	layout.AddWidget(c.rememberSession.QWidget)
	layout.AddWidget(newNote("Skips the browser login while the session is valid; off never stores it.").QWidget)

	addSeparator(layout)
	addCaption(layout, "Server certificate")
	// The old "Trust (accept invalid)" option was removed upstream (see
	// logic.go's certModeLabels doc): only Warn and Pin remain.
	c.certMode, row = comboRow("Verification", certModeLabels)
	c.certMode.OnCurrentIndexChanged(func(int) {
		if c.loading {
			return
		}
		mode := certMode(c.certMode.CurrentText())
		if p := c.activeProfile(); p != nil {
			p.ServerCert.Mode = mode
		}
		c.applyCertMode(mode)
	})
	layout.AddWidget(row)

	c.certPin, c.certPinErrLabel, c.certPinRow = validatedRow("Fingerprint", 0)
	c.certPin.SetPlaceholderText("e.g. sha256:AB:CD:...")
	c.certPin.OnTextChanged(func(s string) {
		markInvalid(c.certPin, c.certPinErrLabel, fingerprintCharset(s))
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.ServerCert.Pin = s
		}
	})
	layout.AddWidget(c.certPinRow)

	addSeparator(layout)
	addCaption(layout, "DNS")
	c.splitDNS = qt.NewQPlainTextEdit2()
	c.splitDNS.SetPlaceholderText("one domain per line, e.g.\ncorp.example.com\ninternal")
	c.splitDNS.SetFixedHeight(90)
	c.splitDNS.OnTextChanged(func() {
		text := c.splitDNS.ToPlainText()
		markInvalid(c.splitDNS, c.splitDNSErrLabel, validateSplitDNSText(text))
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.SplitDNS = parseSplitDNS(text)
		}
	})
	splitDNSForm := qt.NewQFormLayout2()
	splitDNSForm.SetContentsMargins(0, 0, 0, 0)
	splitDNSForm.AddRow3("Split-DNS domains", c.splitDNS.QWidget)
	var splitDNSRow *qt.QWidget
	c.splitDNSErrLabel, splitDNSRow = wrapWithError(splitDNSForm)
	layout.AddWidget(splitDNSRow)
	// The scoped /etc/resolver install/remove that makes these domains
	// resolve through the tunnel goes through the privileged helper (see
	// tunnel.splitDNSEnabled), which is macOS and Linux only — on Windows
	// these domains are stored and not applied, which is the thing a user
	// actually needs told.
	layout.AddWidget(newNote("Looked up through the VPN's DNS. macOS and Linux only; stored but not applied on Windows.").QWidget)

	addSeparator(layout)
	addCaption(layout, "IPsec")

	c.ikeProposalEntry, row = plainRow("IKE proposal")
	c.ikeProposalEntry.OnTextChanged(func(s string) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.IPsec.IKEProposal = s
		}
	})
	layout.AddWidget(row)

	c.espProposalEntry, row = plainRow("ESP proposal")
	c.espProposalEntry.OnTextChanged(func(s string) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.IPsec.ESPProposal = s
		}
	})
	layout.AddWidget(row)

	c.localIDEntry, row = plainRow("Local ID")
	c.localIDEntry.OnTextChanged(func(s string) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.IPsec.LocalID = s
		}
	})
	layout.AddWidget(row)

	c.remoteIDEntry, row = plainRow("Remote ID")
	c.remoteIDEntry.OnTextChanged(func(s string) {
		if c.loading {
			return
		}
		if p := c.activeProfile(); p != nil {
			p.IPsec.RemoteID = s
		}
	})
	layout.AddWidget(row)

	addSeparator(layout)
	addCaption(layout, "Paths")

	c.samlPortEntry, c.samlPortErrLabel, row = validatedRow("SAML redirect port", 150)
	c.samlPortEntry.OnTextChanged(func(s string) {
		err := validatePortString(s)
		markInvalid(c.samlPortEntry, c.samlPortErrLabel, err)
		if c.loading {
			return
		}
		p := c.activeProfile()
		if p == nil {
			return
		}
		if n, perr := parsePort(s); perr == nil {
			p.SAMLPort = n
		} else {
			p.SAMLPort = 0 // flagged invalid; Save's validator catches it
		}
	})
	layout.AddWidget(row)

	c.openconnectPath, c.openconnectPathErrLabel, row = validatedRow("openconnect binary", 0)
	c.openconnectPath.OnTextChanged(func(s string) {
		markInvalid(c.openconnectPath, c.openconnectPathErrLabel, openconnectPathEntryValidator(s))
		if c.loading {
			return
		}
		c.work.OpenconnectPath = s
	})
	layout.AddWidget(row)
	layout.AddWidget(newNote("Only used on Windows; macOS/Linux dial through the privileged helper.").QWidget)

	// Read-only: the sudoers rule is scoped to exactly this path, so editing
	// it here without re-running install.sh would break sudo. Shown for
	// reference only.
	c.helperPath = qt.NewQLabel2()
	helperForm := qt.NewQFormLayout2()
	helperForm.SetContentsMargins(0, 0, 0, 0)
	helperForm.AddRow3("Privileged helper", c.helperPath.QWidget)
	helperRow := qt.NewQWidget(nil)
	helperRow.SetLayout(helperForm.QLayout)
	layout.AddWidget(helperRow)

	helperNote := newNote("Changing this requires re-running scripts/install.sh.")
	setRole(helperNote, "warning")
	layout.AddWidget(helperNote.QWidget)

	layout.AddStretch()
	c.advancedLayout = layout
	return scroll.QWidget
}

// applyCertMode reveals the fingerprint row only when the Pin mode is chosen.
// Its validator tolerates empty so a hidden field never blocks the form.
func (c *Controller) applyCertMode(mode config.ServerCertMode) {
	pin := mode == config.CertPin
	c.certPinRow.SetVisible(pin)
	c.certPin.SetEnabled(pin)
	c.relayout()
}

// updateAuthNote refreshes the auth-method warning shown next to the Method
// combo box. This is only the visual affordance shown before the user even
// tries to Save; the real gate is validateAuthSupported, which Save and
// Connect both run regardless of what this note says.
func (c *Controller) updateAuthNote() {
	p := c.activeProfile()
	if p == nil {
		return
	}
	text := authMethodNoteText(p.Auth.Method)
	c.authNote.SetText(text)
	c.authNoteRow.SetVisible(text != "")
	c.relayout()
}

// updateIPsecAuthVisibility shows the PSK row XOR the cert+key rows based on
// the working copy's chosen IPsec auth method, and hides all three entirely
// when the active profile is not an IPsec profile — an SSL profile has no
// reason to show any IPsec field. Called from the backendSelect and
// ipsecAuthSelect handlers, and from loadProfile.
func (c *Controller) updateIPsecAuthVisibility() {
	p := c.activeProfile()
	if p == nil {
		return
	}
	isIPsec := p.Backend == config.BackendIPsec
	isCert := p.IPsec.AuthMethod == config.IPsecAuthCert
	c.ipsecPSKRow.SetVisible(isIPsec && !isCert)
	c.ipsecCertRow.SetVisible(isIPsec && isCert)
	c.ipsecKeyRow.SetVisible(isIPsec && isCert)
	c.relayout()
}

// relayout forces both tabs' outer layouts to re-run their geometry pass
// after a row's visibility changed. Empirically, Qt's QVBoxLayout already
// reclaims a hidden child's space on its own (verified with a throwaway
// SizeHint() check before/after SetVisible(false) during implementation), so
// this is a defensive no-op in the common case rather than a load-bearing
// workaround — but it costs nothing and protects against a layout that
// someday doesn't.
func (c *Controller) relayout() {
	if c.connectionLayout != nil {
		c.connectionLayout.Activate()
	}
	if c.advancedLayout != nil {
		c.advancedLayout.Activate()
	}
}

func (c *Controller) buildBanner() {
	c.bannerLabel = qt.NewQLabel2()
	c.bannerLabel.SetWordWrap(true)

	dismiss := qt.NewQPushButton3("Dismiss")
	dismiss.OnClicked(c.hideBanner)

	inner := qt.NewQHBoxLayout2()
	inner.SetContentsMargins(12, 8, 12, 8)
	inner.AddWidget(c.bannerLabel.QWidget)
	inner.AddWidget(dismiss.QWidget)

	root := qt.NewQWidget(nil)
	root.SetLayout(inner.QLayout)
	// A subtle warning-colored wash, translucent so it tracks the OS
	// light/dark setting the way the rest of the app's chrome does rather
	// than being a color mixed for only one of them.
	root.SetStyleSheet("background: rgba(224, 161, 64, 0.20); border-radius: 6px;")
	root.SetVisible(false)
	c.banner = root
}

// showBanner fills the banner with msg and reveals it.
func (c *Controller) showBanner(msg string) {
	c.bannerLabel.SetText(msg)
	c.banner.SetVisible(true)
}

// hideBanner dismisses the banner. Wired to the dismiss button and called
// after a successful Save (the issue it named is resolved).
func (c *Controller) hideBanner() {
	c.banner.SetVisible(false)
}

// buildFieldTargets maps every Field* constant (see logic.go) to the
// widget(s) ShowIssue must mark invalid (where the field has an error
// affordance) and focus. Must run after every widget it references has been
// built.
func (c *Controller) buildFieldTargets() {
	c.fieldTargets = map[string]fieldTarget{
		FieldGateway: {
			mark:  func(err error) { markInvalid(c.gatewayEntry, c.gatewayErrLabel, err) },
			focus: c.gatewayEntry.SetFocus,
		},
		FieldPort: {
			mark:  func(err error) { markInvalid(c.portEntry, c.portErrLabel, err) },
			focus: c.portEntry.SetFocus,
		},
		// Selects have no error affordance; the banner names the fix.
		FieldBackend: {focus: c.backendSelect.SetFocus},
		FieldAuth:    {focus: c.authSelect.SetFocus},
		FieldServerCert: {
			mark:  func(err error) { markInvalid(c.certPin, c.certPinErrLabel, err) },
			focus: c.certPin.SetFocus,
		},
		FieldSplitDNS: {
			mark:  func(err error) { markInvalid(c.splitDNS, c.splitDNSErrLabel, err) },
			focus: c.splitDNS.SetFocus,
		},
		FieldIPsecAuth: {focus: c.ipsecAuthSelect.SetFocus},
		FieldIPsecSecret: {
			mark:  func(err error) { markInvalid(c.ipsecSecretEntry, c.ipsecSecretErrLabel, err) },
			focus: c.ipsecSecretEntry.SetFocus,
		},
		// The IPsec cert/key fields are buttons, not entries, so they too are
		// only focused — the banner names the file to choose.
		FieldIPsecCertPath: {focus: c.ipsecCertPathButton.SetFocus},
		FieldIPsecKeyPath:  {focus: c.ipsecKeyPathButton.SetFocus},
	}
}

// ShowIssue reveals the settings content and guides the user straight to the
// field blocking Connect. It re-syncs the form to the saved config first —
// Connect dials what is saved, not any unsaved edits, so the guidance must
// match the saved profile — then selects the issue's profile, switches to its
// tab, marks the offending field invalid and focuses it, and raises the
// persistent banner naming the exact fix.
func (c *Controller) ShowIssue(issue *Issue) {
	if issue == nil {
		return
	}
	c.reset()
	c.sel = c.indexOf(issue.ProfileName)
	c.syncProfileBar()
	c.loadProfile(c.sel)
	c.selectTab(issue.Tab)
	c.markField(issue)
	c.showBanner(issue.Message)
}

// selectTab asks the shell to show the tab an issue lives on.
func (c *Controller) selectTab(tab string) {
	if c.navigate != nil {
		c.navigate(tab)
	}
}

// markField puts the issue's field into its validation-error state (where it
// has one) and focuses it.
func (c *Controller) markField(issue *Issue) {
	target, ok := c.fieldTargets[issue.Field]
	if !ok {
		return
	}
	if target.mark != nil {
		target.mark(errors.New(issue.Message))
	}
	if target.focus != nil {
		target.focus()
	}
}

// clearFieldErrors resets every validated field's error state. Called at the
// start of loadProfile so a stale invalid mark from a previous ShowIssue (for
// a different profile, or a since-fixed value) does not linger on screen for
// a freshly loaded, valid profile.
func (c *Controller) clearFieldErrors() {
	markInvalid(c.nameEntry, c.nameErrLabel, nil)
	markInvalid(c.gatewayEntry, c.gatewayErrLabel, nil)
	markInvalid(c.portEntry, c.portErrLabel, nil)
	markInvalid(c.certPin, c.certPinErrLabel, nil)
	markInvalid(c.splitDNS, c.splitDNSErrLabel, nil)
	markInvalid(c.ipsecSecretEntry, c.ipsecSecretErrLabel, nil)
	markInvalid(c.samlPortEntry, c.samlPortErrLabel, nil)
	markInvalid(c.openconnectPath, c.openconnectPathErrLabel, nil)
}

// loadProfile paints the form from work.Profiles[i]. loading is set for the
// duration so the change handlers do not echo these values back into the
// working copy.
func (c *Controller) loadProfile(i int) {
	if i < 0 || i >= len(c.work.Profiles) {
		return
	}
	c.sel = i
	p := c.work.Profiles[i]
	c.loading = true
	c.clearFieldErrors()

	c.nameEntry.SetText(p.Name)
	c.gatewayEntry.SetText(p.Gateway)
	c.portEntry.SetText(itoa(effectivePort(p.CustomPort, p.Port)))
	c.authSelect.SetCurrentText(authLabel(p.Auth.Method))
	c.backendSelect.SetCurrentText(backendLabel(p.Backend))
	c.autoConnect.SetChecked(c.work.Autostart && c.work.ActiveProfile == p.Name)
	c.keepAlive.SetChecked(p.KeepAlive)

	c.ipsecAuthSelect.SetCurrentText(ipsecAuthLabel(p.IPsec.AuthMethod))
	c.ipsecCertPathEntry.SetText(p.IPsec.CertPath)
	c.ipsecKeyPathEntry.SetText(p.IPsec.KeyPath)
	// Never pre-fill a secret field with a stored value — same convention the
	// SSL password field already follows (were it implemented). The PSK
	// itself lives in credstore, not in the working copy; what this shows is
	// only ever a not-yet-saved edit typed earlier this session for this
	// exact profile (map miss = ""), so browsing away and back does not lose
	// it, and a fresh/never-touched profile still shows blank.
	c.ipsecSecretEntry.SetText(c.ipsecSecretValue[i])

	// Advanced tab.
	c.dualStack.SetChecked(p.DualStack)
	c.dtls.SetChecked(p.DTLS)
	c.rememberSession.SetChecked(p.RememberSession)
	c.certMode.SetCurrentText(certModeLabel(p.ServerCert.Mode))
	c.certPin.SetText(p.ServerCert.Pin)
	c.applyCertMode(p.ServerCert.Mode)
	c.splitDNS.SetPlainText(strings.Join(p.SplitDNS, "\n"))
	c.samlPortEntry.SetText(itoa(effectiveSAMLPort(p.SAMLPort)))
	c.openconnectPath.SetText(effectiveOpenconnectPath(c.work.OpenconnectPath))
	c.helperPath.SetText(c.work.HelperPath)
	c.ikeProposalEntry.SetText(p.IPsec.IKEProposal)
	c.espProposalEntry.SetText(p.IPsec.ESPProposal)
	c.localIDEntry.SetText(p.IPsec.LocalID)
	c.remoteIDEntry.SetText(p.IPsec.RemoteID)

	c.loading = false
	c.updateIPsecAuthVisibility()
	c.updateAuthNote()
}

func (c *Controller) buildActionStrip() *qt.QWidget {
	c.statusText = qt.NewQLabel3("Disconnected")

	// One high-importance action. Save & Reconnect is the same commit plus a
	// tunnel bounce, so it reads as a variant of Save rather than a rival to
	// it; Cancel is quietest because discarding is never what someone came
	// here to do.
	saveBtn := qt.NewQPushButton3("Save")
	saveBtn.OnClicked(func() { c.save(false) })

	c.reconnectBtn = qt.NewQPushButton3("Save & Reconnect")
	c.reconnectBtn.OnClicked(func() { c.save(true) })

	cancelBtn := qt.NewQPushButton3("Cancel")
	cancelBtn.OnClicked(c.cancel)

	c.savedNote = qt.NewQLabel2()
	setRole(c.savedNote, "success")
	c.savedNote.SetVisible(false)

	layout := qt.NewQHBoxLayout2()
	layout.SetContentsMargins(12, 8, 12, 8)
	layout.AddWidget(c.statusText.QWidget)
	layout.AddStretch()
	layout.AddWidget(c.savedNote.QWidget)
	layout.AddWidget(cancelBtn.QWidget)
	layout.AddWidget(c.reconnectBtn.QWidget)
	layout.AddWidget(saveBtn.QWidget)

	root := qt.NewQWidget(nil)
	root.SetLayout(layout.QLayout)
	return root
}

// flashSaved confirms a save in the footer for a couple of seconds. It runs
// entirely on the Qt UI thread via QTimer's own event-loop callback — Save is
// already called there, and the timer's timeout fires there too — so, unlike
// the Fyne version's time.AfterFunc+fyne.Do, no cross-thread marshaling is
// needed at all.
func (c *Controller) flashSaved() {
	if c.savedNote == nil {
		return
	}
	c.savedNote.SetText("Saved")
	c.savedNote.SetVisible(true)

	timer := qt.NewQTimer()
	timer.SetSingleShot(true)
	timer.OnTimeout(func() {
		c.savedNote.SetVisible(false)
		timer.DeleteLater()
	})
	timer.Start(2000)
}

func (c *Controller) save(reconnect bool) {
	// Commit the profile name even if the user has not blurred the field
	// yet — OnEditingFinished only fires on blur/Enter (see the field-mapping
	// table), so Save must also apply whatever is currently typed.
	if c.sel >= 0 && c.sel < len(c.work.Profiles) {
		renameProfile(c.work, c.sel, c.nameEntry.Text())
	}

	work := cloneConfig(c.work)
	normalizePorts(work)
	// Failures land in the inline banner rather than a modal. The banner sits
	// above the very fields the message is about, stays up until the problem
	// is dealt with, and does not have to be dismissed before the user can
	// act on it — all three of which a modal gets wrong.
	if err := validateConfig(work); err != nil {
		c.showBanner(err.Error())
		return
	}
	if err := c.host.Commit(work); err != nil {
		c.showBanner("Could not save: " + err.Error())
		return
	}
	// Persist EVERY profile's IPsec PSK secret with an unsaved edit — not
	// just the one currently shown in the form. Like the SSL password/cookie,
	// a PSK is never stored in config.json — only in credstore, keyed by
	// gateway (config.IPsecPSKCredstoreKey). ipsecSecretDirty/ipsecSecretValue
	// are keyed by profile index specifically so switching the profile
	// dropdown does not lose an edit made to another profile; Save must
	// honor that by writing each dirty entry to ITS OWN profile's credstore
	// key, not just c.sel's.
	for idx, dirty := range c.ipsecSecretDirty {
		if !dirty || idx < 0 || idx >= len(c.work.Profiles) {
			continue // stale/cleared entry; skip rather than index out of range
		}
		profile := c.work.Profiles[idx]
		if profile.Backend != config.BackendIPsec || profile.IPsec.AuthMethod != config.IPsecAuthPSK {
			continue
		}
		if err := credstore.Set(config.IPsecPSKCredstoreKey(profile.Gateway), c.ipsecSecretValue[idx]); err != nil {
			c.showBanner("Could not save the pre-shared key: " + err.Error())
			return
		}
		// Persisted: drop the plaintext from memory rather than leaving it
		// sitting in the map for the life of the window. Safe to delete the
		// current key while ranging over the same map (see the Go spec on
		// map iteration).
		delete(c.ipsecSecretDirty, idx)
		delete(c.ipsecSecretValue, idx)
	}
	// Keep the visible working copy consistent with what was just persisted.
	c.work = cloneConfig(work)
	// The config now validates, so any Connect-issue banner it raised is
	// stale.
	c.hideBanner()
	if reconnect {
		// Reaching a running tunnel with the new settings: tear the current
		// one down and bring it back up so the supervisor re-reads the
		// active profile.
		c.host.Disconnect()
		c.host.Connect()
		return
	}
	c.flashSaved()
}

func (c *Controller) cancel() {
	c.reset()
	c.win.Hide()
}

func (c *Controller) addProfile() {
	name := uniqueName("New profile", c.work.Profiles)
	c.work.Profiles = append(c.work.Profiles, config.NewProfile(name))
	c.sel = len(c.work.Profiles) - 1
	c.syncProfileBar()
	c.loadProfile(c.sel)
	c.nameEntry.SetFocus()
}

func (c *Controller) duplicateProfile() {
	src := c.activeProfile()
	if src == nil {
		return
	}
	dup := *cloneProfile(src)
	dup.Name = uniqueName(src.Name+" copy", c.work.Profiles)
	c.work.Profiles = append(c.work.Profiles, dup)
	c.sel = len(c.work.Profiles) - 1
	c.syncProfileBar()
	c.loadProfile(c.sel)
}

func (c *Controller) deleteProfile() {
	// Guarded by the disabled button; kept as a belt-and-braces return rather
	// than a dialog, because a refusal is not news worth a modal.
	if !canDeleteProfile(len(c.work.Profiles)) {
		return
	}
	victim := c.activeProfile()
	if victim == nil {
		return
	}
	mb := qt.NewQMessageBox3(qt.QMessageBox__Question, "Delete profile",
		"Delete the profile \""+victim.Name+"\"? This cannot be undone.")
	mb.SetStandardButtons(qt.QMessageBox__Yes | qt.QMessageBox__No)
	if mb.Exec() != int(qt.QMessageBox__Yes) {
		return
	}

	removedName := victim.Name
	removed := c.sel
	c.work.Profiles = append(c.work.Profiles[:c.sel], c.work.Profiles[c.sel+1:]...)
	c.reindexIPsecSecrets(removed)
	// If the active profile was removed, fall back to the first one.
	if c.work.ActiveProfile == removedName {
		c.work.ActiveProfile = c.work.Profiles[0].Name
	}
	if c.sel >= len(c.work.Profiles) {
		c.sel = len(c.work.Profiles) - 1
	}
	c.syncProfileBar()
	c.loadProfile(c.sel)
}

// reindexIPsecSecrets drops the not-yet-saved PSK edit (if any) for the
// profile at index removed, and shifts every later index down by one, so
// ipsecSecretDirty/ipsecSecretValue — keyed by profile index — stay aligned
// with c.work.Profiles after deleteProfile shifts everything after removed
// left by one.
func (c *Controller) reindexIPsecSecrets(removed int) {
	dirty := map[int]bool{}
	value := map[int]string{}
	for idx, v := range c.ipsecSecretDirty {
		switch {
		case idx == removed:
			continue
		case idx > removed:
			dirty[idx-1] = v
		default:
			dirty[idx] = v
		}
	}
	for idx, v := range c.ipsecSecretValue {
		switch {
		case idx == removed:
			continue
		case idx > removed:
			value[idx-1] = v
		default:
			value[idx] = v
		}
	}
	c.ipsecSecretDirty = dirty
	c.ipsecSecretValue = value
}

func (c *Controller) setActive() {
	p := c.activeProfile()
	if p == nil {
		return
	}
	c.work.ActiveProfile = p.Name
	c.syncProfileBar()
	// The auto-connect checkbox reflects "this profile is active"; refresh
	// it.
	c.autoConnect.SetChecked(c.work.Autostart && c.work.ActiveProfile == p.Name)
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
