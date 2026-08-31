// Package status is the window that shows what the tunnel is actually doing.
//
// It exists because the tray menu cannot show live state. The tray's only route to
// changing a tray menu is a full teardown-and-rebuild, which the OS ignores while
// the menu is open — so a menu held open through a state change is a snapshot from
// the moment it opened. That limitation is documented at length in internal/tray,
// and an attempt to work around it there froze the menu on both platforms.
//
// A window has no such constraint: it repaints whenever its widgets change. So
// this is the surface where "Reconnecting → Connected" is visible as it happens,
// and the menu goes back to being a short list of actions.
//
// Every method mutates Qt widgets and must run on Qt's UI thread. Apply is called
// from the app's event pump; Tick likewise.
package status

import (
	"fmt"
	"image/color"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
	"github.com/savvaskoualis/openfortitray/internal/uitheme"
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
// far more than the window's own height, so most of the history was simply
// unreachable, with no scrollbar to suggest otherwise.
const activityHeight = 190

// WindowHeight is the window's closed height. The margin above the collapsed
// content's measured size is deliberately generous — a fixed pixel height
// rendered across three OSes' different default font metrics needs slack, or a
// longer gateway string / a slightly taller system font reintroduces the very
// scrollbar this exists to avoid.
const WindowHeight = 600

// emDash is what an unknown value reads as. An empty cell in a two-column card
// looks like a rendering bug; a dash reads as "nothing to report".
const emDash = "—"

// Controller owns the window's content widget and every widget in it.
type Controller struct {
	host Host
	win  *qt.QMainWindow

	// dot is the state badge: a single circle coloured by the current Kind via
	// the "role" QSS property (see uitheme's [role="success"|"warning"|"error"]
	// selectors). It is the only saturated colour in the window when idle.
	// spinner replaces it while v.Busy() (authenticating/connecting/
	// reconnecting) — a static dot reads as inert exactly when something
	// really is happening; only one of the two is ever visible. It is a
	// real rotating ring of pre-rendered frames (see spinner.go), not a
	// generic QProgressBar — a stock indeterminate bar reads as a loading
	// bar, not a status spinner.
	dot          *qt.QLabel
	spinner      *qt.QLabel
	spinnerFrame []*qt.QPixmap
	spinnerTimer *qt.QTimer
	spinnerIdx   int
	// pulse replaces dot while connected (uistate.KindOK): a solid dot with
	// an outer ring that expands and fades, looping — a static "connected"
	// dot reads as inert exactly in the one state that's actually live.
	pulse      *qt.QLabel
	pulseFrame []*qt.QPixmap
	pulseTimer *qt.QTimer
	pulseIdx   int
	stateText  *qt.QLabel
	subText    *qt.QLabel
	// timerText is the session clock, on its own line in the monospace face so a
	// ticking second does not shift the gateway name above it.
	timerText *qt.QLabel

	// The activity disclosure. This is a hand-rolled toggle rather than a fancier
	// widget because opening it has to grow the window: at the default height the
	// expanded list began below the last pixel, so the one thing the user had just
	// asked to see was the one thing not on screen. The count rides on the
	// toggle's own label, because a collapsed section otherwise gives no clue
	// whether it holds fourteen lines or none.
	activityToggle *qt.QToolButton
	activityScroll *qt.QScrollArea
	// activityLayout owns the rows inside activityScroll's widget; refreshActivity
	// clears and repopulates it on every render. activityRows mirrors the labels
	// currently in that layout, newest first, so refreshActivity can tear them
	// down without walking the layout's generic QLayoutItems back into QLabels.
	activityLayout *qt.QVBoxLayout
	activityRows   []*qt.QLabel
	activityOpen   bool

	ipValue    *qt.QLabel
	protoValue *qt.QLabel
	sinceValue *qt.QLabel

	primary *qt.QPushButton
	// primaryAction is what a click on primary currently does. It is read by a
	// SINGLE click handler wired once in build(); setPrimary only ever reassigns
	// this field, never calls OnClicked again — Qt signal connections accumulate
	// rather than replace, so reconnecting on every Apply would eventually fire
	// every state's action on one click.
	primaryAction func()
	logBtn        *qt.QPushButton

	ring *uistate.Ring

	// connectedAt is when the CURRENT session came up, or the zero time when
	// nothing is up. It is set on the first Connected event of a session and
	// cleared on anything else, so an uptime never spans a drop.
	connectedAt time.Time

	// now is injected so the uptime is testable without sleeping.
	now func() time.Time

	// content is the whole panel, handed to the shell.
	content *qt.QWidget
}

// New builds the window's content on the given (not-yet-shown) window and returns
// the controller. The window is left hidden; the tray's Status… item reveals it.
func New(host Host, win *qt.QMainWindow) *Controller {
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
	// One hero (am I connected?), one primary action, and everything else
	// deliberately quieter beneath it. Portrait, sized to its content, centred —
	// the shape every comparable client uses, because the content is a single
	// vertical thought rather than a table.
	root := qt.NewQWidget(nil)
	rootLayout := qt.NewQVBoxLayout2()
	rootLayout.SetContentsMargins(18, 18, 18, 12)
	rootLayout.SetSpacing(14)

	// The state badge: a real filled circle (fixed size + border-radius via
	// QSS), not a text glyph. A Unicode "●" glyph renders inconsistently
	// across fonts/sizes and reads small and thin next to the rest of the
	// hero — a proper circle widget scales and looks the same everywhere.
	c.dot = qt.NewQLabel2()
	c.dot.SetObjectName(*qt.NewQAnyStringView3("statusDot"))
	d := int(uitheme.StatusDotDiameter())
	c.dot.SetFixedSize2(d, d)

	// A neutral, theme-independent tint (macOS's own spinner stays
	// achromatic across light/dark too) — internal/status has no dark-mode
	// flag threaded into it, and a spinner's motion is what reads as
	// "busy", not its exact hue matching the current palette variant.
	c.spinnerFrame = renderSpinnerFrames(color.RGBA{R: 210, G: 210, B: 210, A: 255}, d*2)
	c.spinner = qt.NewQLabel2()
	c.spinner.SetFixedSize2(d*2, d*2)
	c.spinner.SetAlignment(qt.AlignCenter)
	c.spinner.SetPixmap(c.spinnerFrame[0])
	c.spinner.SetVisible(false)
	c.spinnerTimer = qt.NewQTimer2(nil)
	c.spinnerTimer.SetInterval(spinnerTickMS)
	c.spinnerTimer.OnTimeout(func() {
		c.spinnerIdx = (c.spinnerIdx + 1) % len(c.spinnerFrame)
		c.spinner.SetPixmap(c.spinnerFrame[c.spinnerIdx])
	})

	// Apple's systemGreen — matches every other "live" indicator on the
	// platform (recording dot, screen-sharing menu bar icon, etc.).
	pulseGreen := color.RGBA{R: 52, G: 199, B: 89, A: 255}
	c.pulseFrame = renderPulseFrames(pulseGreen, d, d*2)
	c.pulse = qt.NewQLabel2()
	c.pulse.SetFixedSize2(d*2, d*2)
	c.pulse.SetAlignment(qt.AlignCenter)
	c.pulse.SetPixmap(c.pulseFrame[0])
	c.pulse.SetVisible(false)
	c.pulseTimer = qt.NewQTimer2(nil)
	c.pulseTimer.SetInterval(pulseTickMS)
	c.pulseTimer.OnTimeout(func() {
		c.pulseIdx = (c.pulseIdx + 1) % len(c.pulseFrame)
		c.pulse.SetPixmap(c.pulseFrame[c.pulseIdx])
	})

	// The state is the largest thing on screen; the gateway and the clock sit
	// under it in the muted foreground.
	c.stateText = centeredLabel(20, true)
	c.subText = centeredLabel(13, false)
	c.timerText = centeredLabel(13, false)
	monospace(c.timerText)

	heroLayout := qt.NewQVBoxLayout2()
	heroLayout.SetSpacing(2)
	heroLayout.AddWidget3(c.dot.QWidget, 0, qt.AlignHCenter)
	heroLayout.AddWidget3(c.spinner.QWidget, 0, qt.AlignHCenter)
	heroLayout.AddWidget3(c.pulse.QWidget, 0, qt.AlignHCenter)
	heroLayout.AddWidget(c.stateText.QWidget)
	heroLayout.AddWidget(c.subText.QWidget)
	heroLayout.AddWidget(c.timerText.QWidget)
	rootLayout.AddLayout(heroLayout.QLayout)

	// One primary action, centred so it reads as THE thing to press. Wired ONCE
	// here; setPrimary only relabels it and repoints primaryAction (see the field
	// comment for why).
	c.primary = qt.NewQPushButton3("Disconnect")
	c.primary.OnClicked(func() {
		if c.primaryAction != nil {
			c.primaryAction()
		}
	})
	uitheme.Elevate(c.primary.QWidget)
	rootLayout.AddWidget3(c.primary.QWidget, 0, qt.AlignHCenter)

	// Details, deliberately quiet: a muted two-column form under the hero. No
	// Gateway row: the hero already names the gateway directly under the state,
	// and printing it twice in one narrow column is the kind of duplication that
	// makes a window feel padded out rather than composed.
	c.ipValue = cardValue(true)
	c.protoValue = cardValue(false)
	c.sinceValue = cardValue(true)
	details := qt.NewQWidget(nil)
	detailsLayout := qt.NewQFormLayout2()
	detailsLayout.AddRow(cardKey("Assigned IP").QWidget, c.ipValue.QWidget)
	detailsLayout.AddRow(cardKey("Protocol").QWidget, c.protoValue.QWidget)
	detailsLayout.AddRow(cardKey("Connected since").QWidget, c.sinceValue.QWidget)
	// A plain QWidget ignores QSS background/border/padding entirely unless
	// WA_StyledBackground is set — without it, [role="card"]'s fill would be
	// silently a no-op and this would stay invisible flat text, which is
	// exactly the "flat text on a card" complaint this role exists to fix.
	details.SetAttribute2(qt.WA_StyledBackground, true)
	setRole(details, "card")
	uitheme.Elevate(details)
	details.SetLayout(detailsLayout.QLayout)
	rootLayout.AddWidget(details)

	// Only the log remains here. A "Settings…" button became redundant the moment
	// the window grew a navigation rail with Connection and Advanced on it — two
	// ways to reach the same place, one of them pretending to be an action.
	c.logBtn = qt.NewQPushButton3("Open log file…")
	c.logBtn.OnClicked(c.host.OpenLog)
	rootLayout.AddWidget3(c.logBtn.QWidget, 0, qt.AlignHCenter)

	// Activity, collapsed by default. It is the third thing anyone wants and
	// keeping it open forced the window tall enough to strand the rest in
	// whitespace; folded away, the window is the size of its content and expands
	// only when asked.
	c.activityToggle = qt.NewQToolButton2()
	c.activityToggle.SetText("Activity")
	c.activityToggle.SetArrowType(qt.RightArrow)
	c.activityToggle.OnClicked(c.toggleActivity)
	rootLayout.AddWidget(c.activityToggle.QWidget)

	activityContainer := qt.NewQWidget(nil)
	c.activityLayout = qt.NewQVBoxLayout2()
	c.activityLayout.SetContentsMargins(0, 0, 0, 0)
	c.activityLayout.SetSpacing(2)
	activityContainer.SetLayout(c.activityLayout.QLayout)

	c.activityScroll = qt.NewQScrollArea2()
	c.activityScroll.SetWidget(activityContainer)
	c.activityScroll.SetWidgetResizable(true)
	c.activityScroll.SetFixedHeight(activityHeight)
	// Folded away until toggled. Qt's layout system recomputes the window's size
	// hint from visible children automatically once this becomes visible — see
	// toggleActivity, which calls win.AdjustSize() rather than asking the shell
	// for a resize.
	c.activityScroll.SetVisible(false)
	rootLayout.AddWidget(c.activityScroll.QWidget)

	root.SetLayout(rootLayout.QLayout)
	c.content = root
}

// SetClock replaces the time source. It exists for tests, which need a session
// clock that does not move between the setup and the assertion.
func (c *Controller) SetClock(now func() time.Time) { c.now = now }

// Content returns the status panel for the shell to place. This controller no
// longer owns a window — the app has ONE window, and which section it shows is not
// this controller's decision.
func (c *Controller) Content() *qt.QWidget { return c.content }

// centeredLabel builds one of the hero's text lines.
func centeredLabel(pointSize int, bold bool) *qt.QLabel {
	l := qt.NewQLabel2()
	l.SetAlignment(qt.AlignCenter)
	f := l.Font()
	f.SetPointSize(pointSize)
	f.SetBold(bold)
	l.SetFont(f)
	return l
}

// monospace switches a label to a monospace face, so a ticking clock or an IP
// address does not jitter its neighbours as its digits change width.
func monospace(l *qt.QLabel) {
	f := l.Font()
	f.SetStyleHint(qt.QFont__Monospace)
	f.SetFamily("monospace")
	l.SetFont(f)
}

// cardKey is a muted left-column label. It carries the "caption" QSS role, which
// happens to share the same muted colour token as a disabled control.
func cardKey(text string) *qt.QLabel {
	l := qt.NewQLabel3(text)
	setRole(l.QWidget, "caption")
	return l
}

// cardValue is a right-aligned value label. mono is set for anything containing
// digits that change: without it, an IP or a ticking clock jitters its neighbours
// as the digit widths change.
func cardValue(mono bool) *qt.QLabel {
	l := qt.NewQLabel3(emDash)
	l.SetAlignment(qt.AlignRight | qt.AlignVCenter)
	if mono {
		monospace(l)
	}
	return l
}

// setRole sets (or clears, with role == "") the "role" dynamic property that
// uitheme's stylesheet keys its [role="..."] selectors on, then forces Qt to
// re-evaluate the stylesheet against this widget. Qt does NOT automatically
// repolish a widget when an arbitrary dynamic property changes — only the
// unpolish/polish pair below makes an attribute-selector style update take effect
// after the widget has already been shown once.
func setRole(w *qt.QWidget, role string) {
	w.SetProperty("role", qt.NewQVariant11(role))
	if s := w.Style(); s != nil {
		s.Unpolish(w)
		s.Polish(w)
	}
}

// Apply renders one tunnel event.
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

	setRole(c.dot.QWidget, dotRole(v.Kind))
	connected := !v.Busy() && v.Kind == uistate.KindOK
	c.dot.SetVisible(!v.Busy() && !connected)
	c.spinner.SetVisible(v.Busy())
	c.pulse.SetVisible(connected)
	if v.Busy() {
		c.spinnerTimer.Start(spinnerTickMS)
	} else {
		c.spinnerTimer.Stop()
	}
	if connected {
		c.pulseTimer.Start(pulseTickMS)
	} else {
		c.pulseTimer.Stop()
	}

	c.stateText.SetText(v.Title)

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
	c.setSubText(uistate.ViewFor(tunnel.Event{State: tunnel.Connected, Detail: c.ipValue.Text()}))
}

// setSubText writes the line under the state: gateway and uptime when a session
// is up, the state's own short detail otherwise.
func (c *Controller) setSubText(v uistate.View) {
	if !c.connectedAt.IsZero() {
		// Connected: the gateway on one line, the clock on its own beneath it. They
		// used to share a line, which meant the gateway shifted sideways every second
		// as the digits changed width — the kind of jitter that reads as cheap.
		c.subText.SetText(c.host.GatewayLabel())
		c.timerText.SetText(uptime(c.now().Sub(c.connectedAt)))
	} else if v.State == tunnel.Disconnected {
		// Idle: name the gateway this would connect TO. uistate offers "not connected"
		// as a filler, which under a heading that already says "Disconnected" is a
		// tautology occupying the one line that could carry something useful.
		c.subText.SetText(orText(c.host.GatewayLabel(), "no gateway configured"))
		c.timerText.SetText("")
	} else {
		c.subText.SetText(v.Detail)
		c.timerText.SetText("")
	}
}

// setPrimary relabels the one primary button and repoints what a click on it
// does, without reconnecting its click signal (see the primaryAction field
// comment).
//
// Cancel is a distinct label but the same action: tearing the attempt down is what
// stops a browser login or a reconnect loop. Naming it Disconnect there would be a
// lie — there is no connection yet — which is why uistate reports CanDisconnect as
// false for the busy states and this reads Busy() instead.
func (c *Controller) setPrimary(v uistate.View) {
	switch {
	case v.Busy():
		c.primary.SetText("Cancel")
		c.primaryAction = c.host.Disconnect
		setRole(c.primary.QWidget, "danger")
	case v.CanDisconnect:
		c.primary.SetText("Disconnect")
		c.primaryAction = c.host.Disconnect
		setRole(c.primary.QWidget, "danger")
	default:
		c.primary.SetText("Connect")
		c.primaryAction = c.host.Connect
		setRole(c.primary.QWidget, "success")
	}
}

// refreshActivity repaints the history rows. The ring is small and only changes on
// a state transition, so rebuilding these few rows is cheaper than tracking which
// one moved.
func (c *Controller) refreshActivity() {
	// Clear the previous rows before rebuilding: removing from the layout first
	// stops Qt laying out a row that is about to be destroyed, then DeleteLater
	// defers the actual destruction to the event loop rather than freeing a
	// widget Qt may still be mid-paint on.
	for _, row := range c.activityRows {
		c.activityLayout.RemoveWidget(row.QWidget)
		row.DeleteLater()
	}
	c.activityRows = c.activityRows[:0]

	entries := c.ring.Entries()
	now := c.now()
	for _, e := range entries {
		// A bare clock is ambiguous once the app has been up overnight, so anything
		// not from today carries its date. Same-day entries stay uncluttered.
		stamp := e.At.Format("15:04:05")
		if e.At.YearDay() != now.YearDay() || e.At.Year() != now.Year() {
			stamp = e.At.Format("2 Jan 15:04")
		}
		row := qt.NewQLabel3(stamp + " — " + e.Text)
		c.activityLayout.AddWidget(row.QWidget)
		c.activityRows = append(c.activityRows, row)
	}
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
	if c.activityToggle.Text() != title {
		c.activityToggle.SetText(title)
	}
}

// toggleActivity shows or hides the history. It deliberately does NOT resize
// the shared app window: an earlier version called win.AdjustSize() here,
// but c.win is the ONE window the whole shell (nav rail, Connection,
// Advanced, this Status page) lives in, hosted inside a QStackedWidget.
// AdjustSize() recomputes size from the window's overall layout, not just
// this page's content, and in practice collapsed the entire window instead
// of growing it to fit the newly-visible activity list — confirmed live,
// not just a theoretical risk the final review had flagged as unverified.
// activityScroll already has a fixed height (activityHeight), so it simply
// takes its place in the existing layout without needing a window resize.
func (c *Controller) toggleActivity() {
	c.activityOpen = !c.activityOpen
	if c.activityOpen {
		c.activityScroll.SetVisible(true)
		c.activityToggle.SetArrowType(qt.DownArrow)
	} else {
		c.activityScroll.SetVisible(false)
		c.activityToggle.SetArrowType(qt.RightArrow)
	}
}

// dotRole maps a view's severity onto uitheme's semantic QSS roles. These are the
// ONLY place those tokens are used in this window, which is what keeps "the
// tunnel is up" from competing with the accent. KindIdle has no dedicated
// role of its own, so it reuses uitheme's "caption" role — the same muted
// color already used for secondary text — rather than the default foreground
// color, since "not connected" is a de-emphasized, disabled-looking state.
func dotRole(k uistate.Kind) string {
	switch k {
	case uistate.KindOK:
		return "success"
	case uistate.KindBusy:
		return "warning"
	case uistate.KindBad:
		return "error"
	default:
		return "caption"
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
