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
	"image/color"
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

	// stateRing is the outline of the state badge; dot is the solid centre. Both
	// take the state colour, which is the only saturated colour in the window.
	stateRing *canvas.Circle
	dot       *canvas.Circle
	stateText *canvas.Text
	subText   *canvas.Text
	// timerText is the session clock, on its own line in the monospace face so a
	// ticking second does not shift the gateway name above it.
	timerText *canvas.Text
	// accordion holds the activity history, collapsed by default.
	accordion *widget.Accordion

	ipValue    *widget.Label
	protoValue *widget.Label
	sinceValue *widget.Label

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
	// LAYOUT INTENT — a portrait connection panel, not a dashboard.
	//
	// The previous layout was 680x520 landscape and spent roughly the bottom
	// two-fifths on nothing: a VBox of five widgets in a window far taller than they
	// needed, which is the single loudest "unfinished" signal a window can send.
	// Everything also carried the same visual weight, so nothing read as the answer
	// to the only question the window exists to answer.
	//
	// So: one hero (am I connected?), one primary action, and everything else
	// deliberately quieter beneath it. Portrait, sized to its content, centred —
	// the shape every comparable client uses, because the content is a single
	// vertical thought rather than a table.

	// The state badge: a ring in the state colour with a solid dot at its centre.
	// Two canvas circles rather than an image, so it recolours from the theme and
	// costs nothing to redraw on llvmpipe.
	c.stateRing = canvas.NewCircle(color.Transparent)
	c.stateRing.StrokeWidth = 3
	c.dot = canvas.NewCircle(theme.Color(theme.ColorNameDisabled))
	badge := container.NewStack(
		container.New(layout.NewGridWrapLayout(fyne.NewSize(84, 84)), c.stateRing),
		container.NewCenter(container.New(layout.NewGridWrapLayout(fyne.NewSize(18, 18)), c.dot)),
	)

	// The state is the largest thing on screen; the gateway and the clock sit under
	// it in the muted foreground. canvas.Text throughout, because these need an
	// explicit size, colour and alignment and widget.Label offers none of the three.
	c.stateText = centredText(theme.Size(theme.SizeNameHeadingText), true, theme.ColorNameForeground)
	c.subText = centredText(theme.Size(theme.SizeNameText), false, theme.ColorNamePlaceHolder)
	c.timerText = centredText(theme.Size(theme.SizeNameText), false, theme.ColorNamePlaceHolder)
	c.timerText.TextStyle = fyne.TextStyle{Monospace: true}

	hero := container.NewVBox(
		container.NewCenter(badge),
		spacer(10),
		c.stateText,
		spacer(2),
		c.subText,
		c.timerText,
	)

	// One primary action, fixed-width and centred so it reads as THE thing to press
	// and does not resize as its label changes between Connect/Disconnect/Cancel.
	// Built with its widest label for the same min-size reason as before.
	c.primary = widget.NewButton("Disconnect", func() {})
	c.primary.Importance = widget.HighImportance
	primaryRow := container.NewCenter(
		container.New(layout.NewGridWrapLayout(fyne.NewSize(210, 38)), c.primary))

	// Details, deliberately quiet: no border, no card background competing with the
	// hero — just a muted two-column grid under a rule. One FormLayout container so
	// the columns line up across rows.
	c.ipValue = cardValue(true)
	c.protoValue = cardValue(false)
	c.sinceValue = cardValue(true)
	// No Gateway row: the hero already names the gateway directly under the state,
	// and printing it twice in one 400px column is the kind of duplication that makes
	// a window feel padded out rather than composed. It also cost exactly the height
	// the activity section needed to be reachable.
	details := container.New(layout.NewFormLayout(),
		cardKey("Assigned IP"), c.ipValue,
		cardKey("Protocol"), c.protoValue,
		cardKey("Connected since"), c.sinceValue,
	)

	// Secondary actions are low-importance and sit at the foot, where they cannot be
	// mistaken for the primary action. Settings is the more common of the two, so it
	// leads.
	c.settingsBtn = widget.NewButton("Settings…", c.host.ShowSettings)
	c.settingsBtn.Importance = widget.LowImportance
	c.logBtn = widget.NewButton("Open log file…", c.host.OpenLog)
	c.logBtn.Importance = widget.LowImportance
	c.buttonRow = container.NewCenter(container.NewHBox(c.settingsBtn, c.logBtn))

	// Activity, collapsed by default. It is the third thing anyone wants and keeping
	// it open forced the window tall enough to strand the rest in whitespace; folded
	// away, the window is the size of its content and expands only when asked.
	c.activity = container.New(layout.NewFormLayout())
	c.accordion = widget.NewAccordion(widget.NewAccordionItem("Activity", c.activity))

	content := container.NewVBox(
		spacer(18),
		hero,
		spacer(18),
		primaryRow,
		spacer(16),
		widget.NewSeparator(),
		details,
		widget.NewSeparator(),
		spacer(4),
		c.buttonRow,
		c.accordion,
		spacer(4),
	)

	// A tray app's window must never quit the process: that would take the tunnel
	// down with it. Closing hides, exactly as the settings window does.
	c.closeRequested = c.win.Hide
	c.win.SetCloseIntercept(func() { c.closeRequested() })
	c.win.SetContent(container.NewPadded(content))
	c.win.Resize(fyne.NewSize(400, 560))
	c.win.SetFixedSize(false)
}

// centredText builds one of the hero's text lines.
func centredText(size float32, bold bool, name fyne.ThemeColorName) *canvas.Text {
	t := canvas.NewText("", theme.Color(name))
	t.TextSize = size
	t.TextStyle = fyne.TextStyle{Bold: bold}
	t.Alignment = fyne.TextAlignCenter
	return t
}

// spacer is a fixed vertical gap. The layout needs explicit rhythm: a VBox's own
// padding is one value everywhere, and "everything evenly spaced" is precisely what
// made the old window read as a list of widgets rather than a composed screen.
func spacer(h float32) fyne.CanvasObject {
	return container.New(layout.NewGridWrapLayout(fyne.NewSize(1, h)),
		canvas.NewRectangle(color.Transparent))
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

	col := theme.Color(dotColor(v.Kind))
	c.dot.FillColor = col
	c.dot.Refresh()
	// The ring is the same hue at low alpha: present enough to read as a badge,
	// quiet enough not to compete with the state word inside it.
	c.stateRing.StrokeColor = withAlpha(col, 0x66)
	c.stateRing.Refresh()

	c.stateText.Text = v.Title
	c.stateText.Color = theme.Color(theme.ColorNameForeground)
	c.stateText.Refresh()

	c.setSubText(v)

	c.ipValue.SetText(orDash(v.AssignedIP))
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
		// Connected: the gateway on one line, the clock on its own beneath it. They
		// used to share a line, which meant the gateway shifted sideways every second
		// as the digits changed width — the kind of jitter that reads as cheap.
		c.subText.Text = c.host.GatewayLabel()
		c.timerText.Text = uptime(c.now().Sub(c.connectedAt))
	} else if v.State == tunnel.Disconnected {
		// Idle: name the gateway this would connect TO. uistate offers "not connected"
		// as a filler, which under a heading that already says "Disconnected" is a
		// tautology occupying the one line that could carry something useful.
		c.subText.Text = orText(c.host.GatewayLabel(), "no gateway configured")
		c.timerText.Text = ""
	} else {
		c.subText.Text = v.Detail
		c.timerText.Text = ""
	}
	c.subText.Color = theme.Color(theme.ColorNamePlaceHolder)
	c.timerText.Color = theme.Color(theme.ColorNamePlaceHolder)
	c.subText.Refresh()
	c.timerText.Refresh()
}

// withAlpha returns c at the given alpha, for the ring's translucent outline.
func withAlpha(c color.Color, a uint8) color.Color {
	r, g, b, _ := c.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: a}
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

// orText returns s, or alt when s is empty.
func orText(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
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
