// Package status is the window that shows what the tunnel is actually doing.
//
// It exists because the tray menu cannot show live state. fyne's only route to
// changing a tray menu is a full teardown-and-rebuild, which the OS ignores while
// the menu is open — so a menu held open through a state change is a snapshot from
// the moment it opened. That limitation is documented at length in internal/tray,
// and an attempt to work around it there froze the menu on both platforms.
//
// A window has no such constraint: it repaints whenever its widgets change. So
// this is the surface where "Reconnecting → Connected" is visible as it happens,
// and the menu goes back to being a short list of actions.
//
// Every method mutates fyne objects and must run on the UI goroutine. Apply is
// called from inside fyne.Do by the app's event pump; Tick likewise.
package status

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

// Host is everything the window needs from the application.
type Host interface {
	Connect()
	Disconnect()
	// ShowSettings reveals the settings window, which is built once and hidden.
	ShowSettings()
	// OpenLog opens the log file in the platform's default handler.
	OpenLog()
	// GatewayLabel is the active profile's "host:port", for the detail card.
	GatewayLabel() string
	// DTLSLabel is "DTLS on" or "DTLS off" for the active profile.
	DTLSLabel() string
}

// activityRows is the depth of the visible history. Deeper than this and the
// window has to scroll to reach the buttons, which are the reason it is open.
const activityDepth = 12

// emDash is what an unknown value reads as. An empty cell in a two-column card
// looks like a rendering bug; a dash reads as "nothing to report".
const emDash = "—"

// Controller owns the window and every widget in it.
type Controller struct {
	host Host
	win  fyne.Window

	dot       *canvas.Circle
	stateText *canvas.Text
	subText   *canvas.Text

	ipValue      *widget.Label
	gatewayValue *widget.Label
	protoValue   *widget.Label
	sinceValue   *widget.Label

	primary     *widget.Button
	settingsBtn *widget.Button
	logBtn      *widget.Button
	// buttonRow holds the three buttons. Kept so setPrimary can refresh it: a
	// relabelled button reports a new minimum size, but only a container refresh
	// re-runs the layout that acts on it.
	buttonRow *fyne.Container

	activity *fyne.Container
	ring     *uistate.Ring

	// connectedAt is when the CURRENT session came up, or the zero time when
	// nothing is up. It is set on the first Connected event of a session and
	// cleared on anything else, so an uptime never spans a drop.
	connectedAt time.Time

	// now is injected so the uptime is testable without sleeping.
	now func() time.Time

	// closeRequested is what the window's close button runs. Held as a field so a
	// test can invoke it: fyne exposes no getter for a window's close intercept.
	closeRequested func()
}

// New builds the window's content on the given (not-yet-shown) window and returns
// the controller. The window is left hidden; the tray's Status… item calls Show.
func New(host Host, win fyne.Window) *Controller {
	c := &Controller{
		host: host,
		win:  win,
		ring: uistate.NewRing(activityDepth),
		now:  time.Now,
	}
	c.build()
	// Paint the idle state immediately, so a window shown before the first event is
	// not a set of blank rows. This goes through render rather than Apply on
	// purpose: Apply records into the activity history, and a synthetic startup
	// event would put a "Disconnected" line in the log that the tunnel never
	// reported.
	c.render(uistate.ViewFor(tunnel.Event{State: tunnel.Disconnected}))
	return c
}

func (c *Controller) build() {
	// Header. canvas.Text rather than widget.Label: these two need explicit sizes
	// and colours from the theme, and Label offers neither.
	c.dot = canvas.NewCircle(theme.Color(theme.ColorNameDisabled))
	// GridWrap is the layout that gives a canvas object a FIXED cell size; a raw
	// Resize is overwritten the first time the parent lays out, and an unsized
	// circle stretches to fill whatever box it lands in.
	dotBox := container.New(layout.NewGridWrapLayout(fyne.NewSize(11, 11)), c.dot)

	c.stateText = canvas.NewText("", theme.Color(theme.ColorNameForeground))
	c.stateText.TextSize = theme.Size(theme.SizeNameSubHeadingText)
	c.stateText.TextStyle = fyne.TextStyle{Bold: true}

	c.subText = canvas.NewText("", theme.Color(theme.ColorNamePlaceHolder))
	c.subText.TextSize = theme.Size(theme.SizeNameCaptionText)

	header := container.NewHBox(
		container.NewCenter(dotBox),
		container.NewVBox(c.stateText, c.subText),
	)

	// Detail card. One FormLayout container holds every key/value pair so the two
	// columns line up across rows — a per-row container would size each row's key
	// column independently and the values would stagger.
	c.ipValue = cardValue(true)
	c.gatewayValue = cardValue(true)
	c.protoValue = cardValue(false)
	c.sinceValue = cardValue(true)

	rows := container.New(layout.NewFormLayout(),
		cardKey("Assigned IP"), c.ipValue,
		cardKey("Gateway"), c.gatewayValue,
		cardKey("Protocol"), c.protoValue,
		cardKey("Connected since"), c.sinceValue,
	)

	bg := canvas.NewRectangle(theme.Color(theme.ColorNameHeaderBackground))
	bg.CornerRadius = theme.Size(theme.SizeNameCardRadius)
	bg.StrokeColor = theme.Color(theme.ColorNameSeparator)
	bg.StrokeWidth = theme.Size(theme.SizeNameSeparatorThickness)
	card := container.NewStack(bg, container.NewPadded(rows))

	// Buttons. The primary is relabelled per state by Apply; it is the only
	// high-importance control on screen.
	//
	// It is created with its WIDEST label rather than an empty one: a button's
	// minimum size comes from its text, and a container only re-runs its layout when
	// it is refreshed — so a button built empty stayed 44px wide and "Disconnect"
	// spilled out of it. setPrimary refreshes buttonRow for the same reason.
	c.primary = widget.NewButton("Disconnect", func() {})
	c.primary.Importance = widget.HighImportance
	c.settingsBtn = widget.NewButton("Settings…", c.host.ShowSettings)
	c.logBtn = widget.NewButton("Open log file…", c.host.OpenLog)
	c.buttonRow = container.NewHBox(c.primary, layout.NewSpacer(), c.logBtn, c.settingsBtn)

	// Activity. An accordion so the history can be folded away; open by default,
	// because a window whose interesting half is collapsed teaches nobody it is
	// there. One FormLayout container holds the whole history: two columns that line
	// up across rows, and tighter than a stack of per-row HBoxes.
	c.activity = container.New(layout.NewFormLayout())
	acc := widget.NewAccordion(widget.NewAccordionItem("Activity", c.activity))
	acc.Open(0)

	content := container.NewVBox(header, card, c.buttonRow, widget.NewSeparator(), acc)

	// A tray app's window must never quit the process: that would take the tunnel
	// down with it. Closing hides, exactly as the settings window does.
	c.closeRequested = c.win.Hide
	c.win.SetCloseIntercept(func() { c.closeRequested() })
	c.win.SetContent(container.NewPadded(content))
	c.win.Resize(fyne.NewSize(680, 520))
	c.win.SetFixedSize(false)
}

// cardKey is a muted left-column label.
func cardKey(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	return l
}

// cardValue is a right-aligned value label. mono is set for anything containing
// digits that change: fyne has no tabular-figures selector, so the monospace face
// is the only way to stop an IP or a ticking clock from jittering its neighbours.
func cardValue(mono bool) *widget.Label {
	l := widget.NewLabelWithStyle(emDash, fyne.TextAlignTrailing, fyne.TextStyle{Monospace: mono})
	return l
}

// Show reveals and focuses the window.
func (c *Controller) Show() {
	c.win.Show()
	c.win.RequestFocus()
}

// Apply renders one tunnel event.
//
// It mutates the existing widgets rather than rebuilding the tree: llvmpipe is a
// shipping target (Windows VMs and RDP sessions run on bundled Mesa software
// OpenGL) and a relayout on every event is exactly what makes software rendering
// feel slow.
func (c *Controller) Apply(e tunnel.Event) {
	c.ring.Add(e, c.now())
	c.render(uistate.ViewFor(e))
}

// render paints a view. Split from Apply so the constructor can show the idle
// state without writing a synthetic event into the activity history.
func (c *Controller) render(v uistate.View) {
	// Session clock. Set on the first Connected of a session and cleared by
	// anything else, so a reconnect starts from zero rather than counting the
	// downtime, and a re-reported Connected does not restart a long-lived session.
	if v.State == tunnel.Connected {
		if c.connectedAt.IsZero() {
			c.connectedAt = c.now()
		}
	} else {
		c.connectedAt = time.Time{}
	}

	c.dot.FillColor = theme.Color(dotColor(v.Kind))
	c.dot.Refresh()

	c.stateText.Text = v.Title
	c.stateText.Color = theme.Color(theme.ColorNameForeground)
	c.stateText.Refresh()

	c.setSubText(v)

	c.ipValue.SetText(orDash(v.AssignedIP))
	c.gatewayValue.SetText(orDash(c.host.GatewayLabel()))
	c.protoValue.SetText("Fortinet · " + c.host.DTLSLabel())
	if c.connectedAt.IsZero() {
		c.sinceValue.SetText(emDash)
	} else {
		c.sinceValue.SetText(c.connectedAt.Format("15:04"))
	}

	c.setPrimary(v)
	c.refreshActivity()
}

// Tick refreshes the uptime and nothing else. It is called once a second while the
// app runs; when no session is up it returns immediately, so the idle cost is a
// branch rather than a repaint.
func (c *Controller) Tick() {
	if c.connectedAt.IsZero() {
		return
	}
	c.setSubText(uistate.ViewFor(tunnel.Event{State: tunnel.Connected, Detail: c.ipValue.Text}))
}

// setSubText writes the line under the state: gateway and uptime when a session
// is up, the state's own short detail otherwise.
func (c *Controller) setSubText(v uistate.View) {
	if !c.connectedAt.IsZero() {
		host := c.host.GatewayLabel()
		c.subText.Text = host + " · " + uptime(c.now().Sub(c.connectedAt))
	} else {
		c.subText.Text = v.Detail
	}
	c.subText.Color = theme.Color(theme.ColorNamePlaceHolder)
	c.subText.Refresh()
}

// setPrimary relabels and rewires the one high-importance button.
//
// Cancel is a distinct label but the same action: tearing the attempt down is what
// stops a browser login or a reconnect loop. Naming it Disconnect there would be a
// lie — there is no connection yet — which is why uistate reports CanDisconnect as
// false for the busy states and this reads Busy() instead.
func (c *Controller) setPrimary(v uistate.View) {
	switch {
	case v.Busy():
		c.primary.SetText("Cancel")
		c.primary.OnTapped = c.host.Disconnect
	case v.CanDisconnect:
		c.primary.SetText("Disconnect")
		c.primary.OnTapped = c.host.Disconnect
	default:
		c.primary.SetText("Connect")
		c.primary.OnTapped = c.host.Connect
	}
	c.primary.Enable()
	// The label just changed, so the button's minimum size did too; the row has to
	// be re-laid out or the new text renders outside the old box.
	if c.buttonRow != nil {
		c.buttonRow.Refresh()
	}
}

// refreshActivity repaints the history rows. The ring is small and only changes on
// a state transition, so rebuilding these few rows is cheaper than tracking which
// one moved.
func (c *Controller) refreshActivity() {
	entries := c.ring.Entries()
	c.activity.Objects = c.activity.Objects[:0]
	for _, e := range entries {
		ts := widget.NewLabelWithStyle(e.At.Format("15:04:05"), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		ts.Importance = widget.LowImportance
		c.activity.Objects = append(c.activity.Objects, ts, widget.NewLabel(e.Text))
	}
	c.activity.Refresh()
}

// dotColor maps a view's severity onto the semantic tokens. These are the ONLY
// place those tokens are used in this window, which is what keeps "the tunnel is
// up" from competing with the accent.
func dotColor(k uistate.Kind) fyne.ThemeColorName {
	switch k {
	case uistate.KindOK:
		return theme.ColorNameSuccess
	case uistate.KindBusy:
		return theme.ColorNameWarning
	case uistate.KindBad:
		return theme.ColorNameError
	default:
		return theme.ColorNameDisabled
	}
}

func orDash(s string) string {
	if s == "" {
		return emDash
	}
	return s
}

// uptime formats a session duration as HH:MM:SS, with hours allowed to run past
// 24 rather than wrapping — a three-day tunnel should read as 72 hours, not as
// day 3 hour 0.
func uptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}
