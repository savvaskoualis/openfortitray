package settings

import (
	"os"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/credstore"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

func TestMain(m *testing.M) {
	// The offscreen platform plugin is Qt's own documented mechanism for
	// headless test/CI environments — GitHub Actions runners have no logged-in
	// GUI session, so constructing real native windows without it risks a
	// crash during teardown (reproduced directly on two machines before this
	// was added).
	os.Setenv("QT_QPA_PLATFORM", "offscreen")
	qt.NewQApplication(os.Args)
	os.Exit(m.Run())
}

type fakeHost struct {
	cfg      *config.Config
	commits  int
	connects int
	discs    int
}

func (f *fakeHost) Config() *config.Config { return f.cfg }
func (f *fakeHost) Commit(c *config.Config) error {
	f.cfg = c
	f.commits++
	return nil
}
func (f *fakeHost) Connect()    { f.connects++ }
func (f *fakeHost) Disconnect() { f.discs++ }

// minimalConfig returns a valid single-profile config, matching what a fresh
// install's config.json actually looks like (config.defaults()'s shape),
// rather than a wholly empty *config.Config — the settings window is only
// ever built against a config with at least one profile in real use.
func minimalConfig() *config.Config {
	return &config.Config{
		SchemaVersion:   3,
		ActiveProfile:   "Default",
		Profiles:        []config.Profile{config.NewProfile("Default")},
		OpenconnectPath: "openconnect",
		HelperPath:      "/usr/local/libexec/openfortitray-tunnel",
		Autostart:       false,
	}
}

func newTestController(t *testing.T) (*Controller, *fakeHost, *qt.QMainWindow) {
	t.Helper()
	// validateIPsecPSKPresent and Save's credstore.Set both do real I/O;
	// swap in an in-memory fake so tests never touch the real OS keychain
	// (same convention as logic_test.go).
	restore := credstore.SetBackend(credstore.NewMemory())
	t.Cleanup(restore)
	host := &fakeHost{cfg: minimalConfig()}
	win := qt.NewQMainWindow2()
	c := New(host, win)
	return c, host, win
}

func TestContentPanesAreNonNil(t *testing.T) {
	c, _, _ := newTestController(t)
	if c.ConnectionContent() == nil || c.AdvancedContent() == nil || c.Banner() == nil ||
		c.ProfileBar() == nil || c.Footer() == nil {
		t.Fatal("every content pane must be non-nil")
	}
}

func TestShowIssueSwitchesToTheRightTabAndField(t *testing.T) {
	c, _, win := newTestController(t)
	// The error label's IsVisible() reflects Qt's real ancestor-chain
	// visibility, which requires the widget to actually be part of a shown
	// window — an unshown QMainWindow reports IsVisible() == false for every
	// descendant regardless of SetVisible(true) (verified empirically during
	// implementation).
	win.SetCentralWidget(c.AdvancedContent())
	win.Show()
	defer win.Hide()

	var navigated string
	c.SetNavigator(func(tab string) { navigated = tab })

	c.ShowIssue(&Issue{Tab: TabAdvanced, Field: FieldSplitDNS, Message: "bad domain"})

	if navigated != TabAdvanced {
		t.Fatalf("navigated = %q, want %q", navigated, TabAdvanced)
	}
	if !c.splitDNSErrLabel.IsVisible() {
		t.Fatal("split-DNS error label should be visible after ShowIssue")
	}
	if got := c.splitDNSErrLabel.Text(); got != "bad domain" {
		t.Fatalf("split-DNS error label text = %q, want %q", got, "bad domain")
	}
	if !c.banner.IsVisible() {
		t.Fatal("banner should be visible after ShowIssue")
	}
	if got := c.bannerLabel.Text(); got != "bad domain" {
		t.Fatalf("banner text = %q, want %q", got, "bad domain")
	}
}

func TestShowIssueOnGatewayMarksAndFocuses(t *testing.T) {
	c, _, win := newTestController(t)
	win.SetCentralWidget(c.ConnectionContent())
	win.Show()
	defer win.Hide()

	c.SetNavigator(func(string) {})
	c.ShowIssue(&Issue{Tab: TabBasic, Field: FieldGateway, Message: "enter a gateway"})

	if !c.gatewayErrLabel.IsVisible() {
		t.Fatal("gateway error label should be visible")
	}
	if got := c.gatewayErrLabel.Text(); got != "enter a gateway" {
		t.Fatalf("gateway error label text = %q, want %q", got, "enter a gateway")
	}
}

// TestProtocolComboMatchesLogicLabels guards against the Protocol combo box's
// items drifting from logic.go's backendLabels/backendFromLabel/backendLabel
// round-trip. It intentionally does NOT hardcode "SSL VPN"/"IPsec (IKEv2)" —
// verified against the pre-migration Fyne source, the real labels are "SSL
// VPN" and "IPsec" (backendIPsecLabel in logic.go); selecting a
// human-readable-but-wrong label here would silently fail backendFromLabel's
// exact-string match and always resolve to BackendSSL.
func TestProtocolComboMatchesLogicLabels(t *testing.T) {
	c, _, _ := newTestController(t)
	if got := c.backendSelect.CurrentText(); got != backendLabel(config.BackendSSL) {
		t.Fatalf("initial protocol label = %q, want %q", got, backendLabel(config.BackendSSL))
	}
	c.backendSelect.SetCurrentText(backendIPsecLabel)
	if got := c.work.Profiles[c.sel].Backend; got != config.BackendIPsec {
		t.Fatalf("selecting %q did not round-trip to BackendIPsec, got %v", backendIPsecLabel, got)
	}
}

func TestLoadProfilePopulatesFields(t *testing.T) {
	c, host, _ := newTestController(t)
	host.cfg.Profiles[0].Gateway = "vpn.example.com"
	host.cfg.Profiles[0].Port = 4443
	host.cfg.Profiles[0].CustomPort = true
	host.cfg.Profiles[0].SplitDNS = []string{"corp.example.com", "internal"}
	c.reset()

	if got := c.gatewayEntry.Text(); got != "vpn.example.com" {
		t.Errorf("gatewayEntry.Text() = %q, want vpn.example.com", got)
	}
	if got := c.portEntry.Text(); got != "4443" {
		t.Errorf("portEntry.Text() = %q, want 4443", got)
	}
	if got := c.splitDNS.ToPlainText(); got != "corp.example.com\ninternal" {
		t.Errorf("splitDNS.ToPlainText() = %q, want %q", got, "corp.example.com\ninternal")
	}
}

// TestCheckboxTogglesUseSignalCheckedParam confirms the checkbox handlers
// read the toggled signal's own bool parameter (which Qt may report AFTER
// the widget's internal state has already flipped) rather than a
// pre-toggle IsChecked() read — the same class of bug Task 6 found in the
// tray's checkable actions.
func TestCheckboxTogglesUseSignalCheckedParam(t *testing.T) {
	c, _, _ := newTestController(t)

	c.keepAlive.SetChecked(true)
	if !c.work.Profiles[c.sel].KeepAlive {
		t.Fatal("checking Keep VPN up must write true through to the working profile")
	}
	c.keepAlive.SetChecked(false)
	if c.work.Profiles[c.sel].KeepAlive {
		t.Fatal("unchecking Keep VPN up must write false through to the working profile")
	}

	c.dtls.SetChecked(true)
	if !c.work.Profiles[c.sel].DTLS {
		t.Fatal("checking DTLS must write true through")
	}

	c.dualStack.SetChecked(true)
	if !c.work.Profiles[c.sel].DualStack {
		t.Fatal("checking IPv6/dual-stack must write true through")
	}

	c.rememberSession.SetChecked(false)
	if c.work.Profiles[c.sel].RememberSession {
		t.Fatal("unchecking Remember session must write false through")
	}
}

func TestMarkInvalidShowsAndClearsError(t *testing.T) {
	c, _, win := newTestController(t)
	win.SetCentralWidget(c.ConnectionContent())
	win.Show()
	defer win.Hide()

	c.gatewayEntry.SetText("https://not-a-bare-host")
	if !c.gatewayErrLabel.IsVisible() {
		t.Fatal("an invalid gateway must show its error label")
	}

	c.gatewayEntry.SetText("vpn.example.com")
	if c.gatewayErrLabel.IsVisible() {
		t.Fatal("a valid gateway must clear its error label")
	}
}

func TestApplyUpdatesStatusAndReconnectButton(t *testing.T) {
	c, _, _ := newTestController(t)

	c.Apply(tunnel.Event{State: tunnel.Connected})
	if !c.reconnectBtn.IsEnabled() {
		t.Fatal("Save & Reconnect must be enabled while a tunnel is up")
	}

	c.Apply(tunnel.Event{State: tunnel.Disconnected})
	if c.reconnectBtn.IsEnabled() {
		t.Fatal("Save & Reconnect must be disabled while nothing is up")
	}
}

func TestAddDuplicateDeleteProfile(t *testing.T) {
	c, _, _ := newTestController(t)

	c.addProfile()
	if len(c.work.Profiles) != 2 {
		t.Fatalf("after addProfile, len(Profiles) = %d, want 2", len(c.work.Profiles))
	}

	c.duplicateProfile()
	if len(c.work.Profiles) != 3 {
		t.Fatalf("after duplicateProfile, len(Profiles) = %d, want 3", len(c.work.Profiles))
	}

	if !c.delBtn.IsEnabled() {
		t.Fatal("delete button should be enabled with more than one profile")
	}
}

func TestDeleteLastProfileIsANoOp(t *testing.T) {
	c, _, _ := newTestController(t)
	if len(c.work.Profiles) != 1 {
		t.Fatalf("setup: want exactly one profile, got %d", len(c.work.Profiles))
	}
	if c.delBtn.IsEnabled() {
		t.Fatal("delete button must be disabled with only one profile")
	}
	c.deleteProfile() // guarded no-op; must not panic or shrink below 1
	if len(c.work.Profiles) != 1 {
		t.Fatalf("deleteProfile on the last profile must be a no-op, got %d profiles", len(c.work.Profiles))
	}
}

func TestIPsecAuthVisibilityToggles(t *testing.T) {
	c, _, win := newTestController(t)
	win.SetCentralWidget(c.ConnectionContent())
	win.Show()
	defer win.Hide()

	// SSL profile: none of the three rows show.
	if c.ipsecPSKRow.IsVisible() || c.ipsecCertRow.IsVisible() || c.ipsecKeyRow.IsVisible() {
		t.Fatal("an SSL profile must show no IPsec auth rows")
	}

	c.backendSelect.SetCurrentText(backendIPsecLabel)
	if !c.ipsecPSKRow.IsVisible() {
		t.Fatal("an IPsec/PSK profile must show the PSK row")
	}
	if c.ipsecCertRow.IsVisible() || c.ipsecKeyRow.IsVisible() {
		t.Fatal("an IPsec/PSK profile must not show the cert/key rows")
	}

	c.ipsecAuthSelect.SetCurrentText(ipsecCertLabel)
	if c.ipsecPSKRow.IsVisible() {
		t.Fatal("switching to certificate auth must hide the PSK row")
	}
	if !c.ipsecCertRow.IsVisible() || !c.ipsecKeyRow.IsVisible() {
		t.Fatal("switching to certificate auth must show the cert/key rows")
	}
}

func TestSaveCommitsWorkingCopyAndClearsBanner(t *testing.T) {
	c, host, win := newTestController(t)
	win.SetCentralWidget(c.ConnectionContent())
	win.Show()
	defer win.Hide()

	c.gatewayEntry.SetText("vpn.example.com")
	c.showBanner("stale issue")

	c.save(false)

	if host.commits != 1 {
		t.Fatalf("host.commits = %d, want 1", host.commits)
	}
	if host.cfg.Profiles[0].Gateway != "vpn.example.com" {
		t.Fatalf("committed gateway = %q, want vpn.example.com", host.cfg.Profiles[0].Gateway)
	}
	if c.banner.IsVisible() {
		t.Fatal("a successful save must hide the banner")
	}
}

func TestSaveAndReconnectDrivesHost(t *testing.T) {
	c, host, _ := newTestController(t)
	c.gatewayEntry.SetText("vpn.example.com")

	c.save(true)

	if host.discs != 1 || host.connects != 1 {
		t.Fatalf("Save & Reconnect must Disconnect then Connect, got discs=%d connects=%d", host.discs, host.connects)
	}
}

func TestPSKSecretPersistedThroughCredstoreOnSave(t *testing.T) {
	c, _, _ := newTestController(t)
	c.gatewayEntry.SetText("vpn.example.com")
	c.backendSelect.SetCurrentText(backendIPsecLabel)
	c.ipsecAuthSelect.SetCurrentText(ipsecPSKLabel)
	c.ipsecSecretEntry.SetText("s3cret")

	if !c.ipsecSecretDirty[c.sel] {
		t.Fatal("typing a PSK must mark it dirty for the current profile")
	}

	c.save(false)

	got, err := credstore.Get(config.IPsecPSKCredstoreKey("vpn.example.com"))
	if err != nil {
		t.Fatalf("credstore.Get: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("stored PSK = %q, want s3cret", got)
	}
	if c.ipsecSecretDirty[c.sel] {
		t.Fatal("a persisted PSK edit must be cleared from the dirty map")
	}
}

// TestResetClearsUnsavedIPsecSecret confirms reset() (called by both Show and
// Cancel) discards a typed-but-unsaved IPsec PSK, matching its documented
// contract of discarding "any edits left from a previous session" across the
// whole working copy — not just the profile currently on screen. Before
// ipsecSecretDirty/ipsecSecretValue became per-profile maps, loadProfile
// unconditionally blanked the (then-flat) PSK field on every call, so
// Cancel/Show got this "discard" behavior for free as a side effect of the
// very bug that fix corrected; now that loadProfile reads from the maps
// instead of blindly blanking, reset() has to clear them itself. Ported from
// the pre-migration Fyne render_capture_test.go onto the new Qt Controller.
func TestResetClearsUnsavedIPsecSecret(t *testing.T) {
	restore := credstore.SetBackend(credstore.NewMemory())
	t.Cleanup(restore)

	work := config.NewProfile("Work")
	work.Gateway = "vpn.example.com"
	work.Backend = config.BackendIPsec
	work.IPsec.AuthMethod = config.IPsecAuthPSK
	cfg := &config.Config{ActiveProfile: "Work", Profiles: []config.Profile{work}}

	c := New(&fakeHost{cfg: cfg}, qt.NewQMainWindow2())

	c.ipsecSecretEntry.SetText("typed-but-not-saved")
	if !c.ipsecSecretDirty[c.sel] {
		t.Fatal("typing into the PSK entry should have marked it dirty")
	}
	if c.ipsecSecretValue[c.sel] != "typed-but-not-saved" {
		t.Fatalf("ipsecSecretValue[%d] = %q, want the typed text", c.sel, c.ipsecSecretValue[c.sel])
	}

	c.reset() // what both Show and Cancel do

	if got := c.ipsecSecretEntry.Text(); got != "" {
		t.Errorf("after reset, the PSK entry shows %q, want blank", got)
	}
	if c.ipsecSecretDirty[c.sel] {
		t.Error("after reset, the PSK entry should no longer be marked dirty")
	}
	if v, ok := c.ipsecSecretValue[c.sel]; ok {
		t.Errorf("after reset, ipsecSecretValue still holds %q for profile %d, want the map cleared", v, c.sel)
	}
}

// TestSavePersistsAllDirtyIPsecPSKsNotJustSelected: typing a PSK for profile
// A, switching to profile B (which — per ipsecSecretDirty/ipsecSecretValue
// being per-profile maps — must NOT lose A's unsaved edit), typing a
// different PSK for B, then hitting Save must persist BOTH profiles' PSKs to
// their own credstore keys, not just the one shown in the form when Save was
// clicked. Ported from the pre-migration Fyne render_capture_test.go onto
// the new Qt Controller.
func TestSavePersistsAllDirtyIPsecPSKsNotJustSelected(t *testing.T) {
	restore := credstore.SetBackend(credstore.NewMemory())
	t.Cleanup(restore)

	profA := config.NewProfile("A")
	profA.Gateway = "a.example.com"
	profA.Backend = config.BackendIPsec
	profA.IPsec.AuthMethod = config.IPsecAuthPSK

	profB := config.NewProfile("B")
	profB.Gateway = "b.example.com"
	profB.Backend = config.BackendIPsec
	profB.IPsec.AuthMethod = config.IPsecAuthPSK

	cfg := &config.Config{ActiveProfile: "A", Profiles: []config.Profile{profA, profB}}

	c := New(&fakeHost{cfg: cfg}, qt.NewQMainWindow2())

	// Type A's PSK (profile A is shown first, matching ActiveProfile), switch
	// to B, and type a different PSK there.
	c.ipsecSecretEntry.SetText("psk-for-A")
	c.loadProfile(1)
	c.ipsecSecretEntry.SetText("psk-for-B")

	c.save(false)

	gotA, err := credstore.Get(config.IPsecPSKCredstoreKey("a.example.com"))
	if err != nil {
		t.Fatalf("credstore.Get(A): %v", err)
	}
	if gotA != "psk-for-A" {
		t.Errorf("profile A's PSK = %q, want %q — an edit made before switching to B must survive Save", gotA, "psk-for-A")
	}
	gotB, err := credstore.Get(config.IPsecPSKCredstoreKey("b.example.com"))
	if err != nil {
		t.Fatalf("credstore.Get(B): %v", err)
	}
	if gotB != "psk-for-B" {
		t.Errorf("profile B's PSK = %q, want %q", gotB, "psk-for-B")
	}
	if len(c.ipsecSecretDirty) != 0 {
		t.Errorf("ipsecSecretDirty has %d leftover entries after Save, want all cleared: %v", len(c.ipsecSecretDirty), c.ipsecSecretDirty)
	}
	if len(c.ipsecSecretValue) != 0 {
		t.Errorf("ipsecSecretValue has %d leftover entries after Save, want the plaintext dropped from memory: %v", len(c.ipsecSecretValue), c.ipsecSecretValue)
	}
}
