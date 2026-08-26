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

// activityDepth is how much history is kept. It was 12 — chosen only because the
// list had no scroller and anything longer pushed the window past the screen. Now
// that it scrolls, the number can be what is actually useful: a flapping tunnel
// burns through a dozen transitions in a minute or two, which is exactly when
// someone opens this.
const activityDepth = 50

// activityHeight bounds the expanded list. Without a bound the content demanded
// 1061px inside a 560px window — measured, not guessed — so most of the history was
// simply unreachable, with no scrollbar to suggest otherwise.
const activityHeight = 190

// WindowHeight is the window's closed height, grown by exactly activityHeight when
// the disclosure opens (see toggleActivity). shell.go uses this as the Status
// section's base height too, rather than a second hardcoded number: the collapsed
// content's real MinSize (measured headless) is ~521px, and a single guess shared
// by both packages is the only way it stays true window-to-window instead of two
// numbers someone has to remember to keep in sync. The margin above that measured
// size is deliberately generous — a fixed pixel height rendered across three OSes'
// different default font metrics needs slack, or a longer gateway string / a
// slightly taller system font reintroduces the very scrollbar this exists to avoid.
const WindowHeight = 600

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
	// The activity disclosure. This is a hand-rolled toggle rather than a
	// widget.Accordion because the accordion cannot report WHEN it was opened, and
	// opening it has to grow the window: at the default height the expanded list
	// began below the last pixel, so the one thing the user had just asked to see was
	// the one thing not on screen. The count rides on the toggle's own label, because
	// a collapsed section otherwise gives no clue whether it holds fourteen lines or
	// none.
	activityToggle *widget.Button
	activityScroll *container.Scroll
	activityOpen   bool

	ipValue    *widget.Label
	protoValue *widget.Label
	sinceValue *widget.Label

	primary *widget.Button
	logBtn  *widget.Button
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

	// content is the whole panel, handed to the shell.
	content fyne.CanvasObject

	// OnHeightRequest asks the shell to make the window this tall, so revealing the
	// history opens space for it instead of pushing it past the bottom edge. The
	// shell owns the window, so it owns the resize.
	OnHeightRequest func(h float32)
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

	// Only the log remains here. A "Settings…" button became redundant the moment the
	// window grew a navigation rail with Connection and Advanced on it — two ways to
	// reach the same place, one of them pretending to be an action.
	c.logBtn = widget.NewButton("Open log file…", c.host.OpenLog)
	c.logBtn.Importance = widget.LowImportance
	c.buttonRow = container.NewCenter(c.logBtn)

	// Activity, collapsed by default. It is the third thing anyone wants and keeping
	// it open forced the window tall enough to strand the rest in whitespace; folded
	// away, the window is the size of its content and expands only when asked.
	c.activity = container.New(layout.NewFormLayout())
	c.activityScroll = container.NewVScroll(c.activity)
	c.activityScroll.SetMinSize(fyne.NewSize(0, activityHeight))
	c.activityScroll.Hide()
	c.activityToggle = widget.NewButtonWithIcon("Activity", theme.MenuExpandIcon(), c.toggleActivity)
	c.activityToggle.Importance = widget.LowImportance
	c.activityToggle.Alignment = widget.ButtonAlignLeading

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
		c.activityToggle,
		c.activityScroll,
		spacer(4),
	)

	// An outer scroller as the backstop: expanding a section, a long error line or a
	// small display must never put a control out of reach with no way to get at it.
	c.content = container.NewVScroll(container.NewPadded(content))
}

// SetClock replaces the time source. It exists for tests and renders, which need a
// session clock that does not move between the setup and the assertion.
func (c *Controller) SetClock(now func() time.Time) { c.now = now }

// Content returns the status panel for the shell to place. This controller no
// longer owns a window — the app has ONE window, and which section it shows is not
// this controller's decision.
func (c *Controller) Content() fyne.CanvasObject { return c.content }

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

// Show is retained for symmetry with the settings controller and is a no-op:
// revealing the single window is the shell's job.
func (c *Controller) Show() {}

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
	now := c.now()
	for _, e := range entries {
		// A bare clock is ambiguous once the app has been up overnight, so anything
		// not from today carries its date. Same-day entries stay uncluttered.
		stamp := e.At.Format("15:04:05")
		if e.At.YearDay() != now.YearDay() || e.At.Year() != now.Year() {
			stamp = e.At.Format("2 Jan 15:04")
		}
		ts := widget.NewLabelWithStyle(stamp, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		ts.Importance = widget.LowImportance
		c.activity.Objects = append(c.activity.Objects, ts, widget.NewLabel(e.Text))
	}
	c.activity.Refresh()
	c.setActivityTitle(len(entries))
}

// setActivityTitle puts the entry count on the collapsed toggle, so it advertises
// whether there is anything inside. "Activity" alone said nothing either way.
func (c *Controller) setActivityTitle(n int) {
	if c.activityToggle == nil {
		return
	}
	title := "Activity"
	if n > 0 {
		title = fmt.Sprintf("Activity (%d)", n)
	}
	if c.activityToggle.Text != title {
		c.activityToggle.SetText(title)
	}
}

// toggleActivity shows or hides the history AND resizes the window by the same
// amount, so the list appears in space that was made for it rather than below the
// bottom edge.
func (c *Controller) toggleActivity() {
	c.activityOpen = !c.activityOpen
	h := WindowHeight
	if c.activityOpen {
		c.activityScroll.Show()
		c.activityToggle.SetIcon(theme.MenuDropDownIcon())
		h += activityHeight
	} else {
		c.activityScroll.Hide()
		c.activityToggle.SetIcon(theme.MenuExpandIcon())
	}
	if c.OnHeightRequest != nil {
		c.OnHeightRequest(float32(h))
	}
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
