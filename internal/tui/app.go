package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ana-rodrigues/wap/internal/whatsapp"
)

// Screen identifies the currently active UI screen.
type Screen int

const (
	ScreenAuth Screen = iota
	ScreenContacts
	ScreenChat
)

// keep unexported aliases for internal use
const (
	screenAuth     = ScreenAuth
	screenContacts = ScreenContacts
	screenChat     = ScreenChat
)

// connectionState tracks WhatsApp connection status for the status bar.
type connectionState int

const (
	connConnected connectionState = iota
	connDisconnected
)

var reconnectPalette = []lipgloss.Color{
	"#004D20", "#006B2C", "#008F3A", "#00B347", "#00D455", "#00E676",
}

var statusConnectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))

// tickMsg drives the reconnect animation.
type tickMsg time.Time

// App is the top-level Bubble Tea model.
type App struct {
	client  *whatsapp.Client
	noEmoji bool
	width   int
	height  int

	active    Screen
	connState connectionState
	tickFrame int

	auth       AuthScreen
	contacts   ContactsScreen
	chat       ChatScreen
	cmdBar     CommandBar
	cmdBarOpen bool
}

// New creates the root App model.
func New(client *whatsapp.Client, startScreen Screen, noEmoji bool) App {
	return App{
		client:   client,
		noEmoji:  noEmoji,
		active:   startScreen,
		auth:     NewAuthScreen(),
		contacts: NewContactsScreen(0, 0),
		cmdBar:   NewCommandBar(),
	}
}

func (m App) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.client), m.contacts.Init())
}

// SendResultMsg carries the result of an async SendText operation.
type SendResultMsg struct {
	Text string
	Err  error
}

func (m App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	// ── Spinner tick forwarded to contacts while loading ─────────────────────
	case spinner.TickMsg:
		if m.contacts.syncing {
			var cmd tea.Cmd
			m.contacts, cmd = m.contacts.Update(msg)
			cmds = append(cmds, cmd)
		}

	// ── Window size ──────────────────────────────────────────────────────────
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Delegate to screens so they resize without losing state.
		// Never recreate a screen here — that wipes populated data.
		var resizeCmd tea.Cmd
		m.contacts, resizeCmd = m.contacts.Update(msg)
		cmds = append(cmds, resizeCmd)
		m.chat, resizeCmd = m.chat.Update(msg)
		cmds = append(cmds, resizeCmd)

	// ── Keyboard ─────────────────────────────────────────────────────────────
	case tea.KeyMsg:
		// Global: Ctrl+C → quit (preserve session)
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}

		// Global: Option+Esc → logout (clear session) and quit
		// Terminal reports: KeyEsc with Alt=true
		if msg.Type == tea.KeyEsc && msg.Alt {
			_ = m.client.Logout()
			return m, tea.Quit
		}

		// '/' opens command bar (unless already open or in textinput)
		if !m.cmdBarOpen && msg.Type == tea.KeyRunes && msg.String() == "/" {
			m.cmdBarOpen = true
			m.cmdBar = NewCommandBar()
			return m, nil
		}

		if m.cmdBarOpen {
			var cmd tea.Cmd
			m.cmdBar, cmd = m.cmdBar.Update(msg)
			return m, cmd
		}

		// Esc (no Alt) — context-dependent navigation
		if msg.Type == tea.KeyEsc && !msg.Alt {
			switch {
			// Esc on recents → quit (preserve session)
			case m.active == screenContacts && !m.contacts.compact:
				return m, tea.Quit
			// Esc on all-contacts → back to recents
			case m.active == screenContacts && m.contacts.compact:
				m.contacts = NewContactsScreen(m.width, m.height)
				cmds = append(cmds, m.contacts.Init())
				m.contacts = m.contacts.Populate(m.client.RecentChats(5), nil)
				return m, tea.Batch(cmds...)
			// Esc in chat → back to contacts
			case m.active == screenChat:
				// Sync accumulated messages back to client before leaving
				m.client.SyncMessages(m.chat.contact.JID, m.chat.Messages())
				m.active = screenContacts
				m.contacts = NewContactsScreen(m.width, m.height)
				cmds = append(cmds, m.contacts.Init())
				m.contacts = m.contacts.Populate(m.client.RecentChats(5), nil)
				return m, tea.Batch(cmds...)
			}
		}

		// Delegate to active screen
		switch m.active {
		case screenContacts:
			var cmd tea.Cmd
			m.contacts, cmd = m.contacts.Update(msg)
			cmds = append(cmds, cmd)
		case screenChat:
			var cmd tea.Cmd
			m.chat, cmd = m.chat.Update(msg)
			cmds = append(cmds, cmd)
		}

	// ── Command bar actions ───────────────────────────────────────────────────
	case ShowAllContactsMsg:
		m.cmdBarOpen = false
		m.active = screenContacts
		m.contacts = NewCompactContactsScreen(m.width, m.height)
		m.contacts = m.contacts.Populate(nil, m.client.Contacts())
		return m, nil

	case QuitAppMsg:
		m.cmdBarOpen = false
		return m, tea.Quit

	case LogoutMsg:
		m.cmdBarOpen = false
		_ = m.client.Logout()
		return m, tea.Quit

	case DismissCommandBarMsg:
		m.cmdBarOpen = false

	// ── Contact selected → open chat ─────────────────────────────────────────
	case ContactSelectedMsg:
		m.client.MarkRead(msg.Contact.JID)
		m.chat = NewChatScreen(msg.Contact, m.client, m.noEmoji, m.width, m.height)
		m.active = screenChat

	// ── Outgoing message ──────────────────────────────────────────────────────
	case SendTextMsg:
		// Show message immediately (optimistic rendering)
		m.chat = m.chat.AddSentMessage(msg.Text)
		// Send to WhatsApp asynchronously (non-blocking)
		jid := m.chat.contact.JID
		text := msg.Text
		cmds = append(cmds, func() tea.Msg {
			err := m.client.SendText(jid, text)
			return SendResultMsg{Text: text, Err: err}
		})

	// ── Async send result ───────────────────────────────────────────────────
	case SendResultMsg:
		if msg.Err != nil && m.active == screenChat {
			m.chat = m.chat.AddFailedMessage(msg.Text)
		}

	// ── WhatsApp events ───────────────────────────────────────────────────────
	case WaitForEventMsg:
		cmds = append(cmds, waitForEvent(m.client)) // keep listening

		switch msg.Event.Kind {
		case whatsapp.EventQRCode:
			if code, ok := msg.Event.Payload.(string); ok {
				m.auth = m.auth.SetQR(code)
			}

		case whatsapp.EventConnected:
			m.connState = connConnected
			if m.active == screenAuth {
				m.active = screenContacts
				m.contacts = NewContactsScreen(m.width, m.height)
				cmds = append(cmds, m.contacts.Init())
			}

		case whatsapp.EventContactsReady:
			if m.active == screenContacts && !m.contacts.compact {
				m.contacts = m.contacts.Populate(
					m.client.RecentChats(5),
					nil,
				)
			}

		case whatsapp.EventDisconnected:
			m.connState = connDisconnected
			cmds = append(cmds, animTick())

		case whatsapp.EventMessage:
			// Forward message events to chat screen for real-time updates
			if m.active == screenChat {
				var cmd tea.Cmd
				m.chat, cmd = m.chat.Update(msg)
				cmds = append(cmds, cmd)
			}
			// Refresh contacts list in real-time, but ONLY after the initial
			// load is complete (syncing == false). This updates previews and
			// reorders chats without interrupting the loading spinner.
			if m.active == screenContacts && !m.contacts.compact && !m.contacts.syncing {
				m.contacts = m.contacts.Populate(m.client.RecentChats(5), nil)
			}
		}

	// ── Reconnect animation tick ──────────────────────────────────────────────
	case tickMsg:
		if m.connState == connDisconnected {
			m.tickFrame = (m.tickFrame + 1) % len(reconnectPalette)
			cmds = append(cmds, animTick())
		}
	}

	return m, tea.Batch(cmds...)
}

var (
	hintDividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	hintKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))
	hintLabelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
)

func (m App) View() string {
	var body string
	switch m.active {
	case screenAuth:
		body = m.auth.View()
	case screenContacts:
		body = m.contacts.View()
	case screenChat:
		body = m.chat.View()
	}

	statusBar := m.renderStatusBar()
	hint := m.renderHint()

	// Build the footer (hint bar + optional command bar + status bar)
	var footer string
	if m.cmdBarOpen {
		footer = hint + m.cmdBar.View() + "\n" + statusBar
	} else {
		footer = hint + statusBar
	}

	// Footer is always: 1 (divider) + 1 (hint) + 1 (status) + optional cmd bar
	footerHeight := 3
	if m.cmdBarOpen {
		footerHeight += 2 // command bar takes ~2 lines
	}

	// Pad body to fill available height, pushing footer to bottom
	availableForBody := m.height - footerHeight
	bodyLines := strings.Count(body, "\n") + 1
	if bodyLines < availableForBody {
		body += strings.Repeat("\n", availableForBody-bodyLines)
	}

	return body + footer
}

func (m App) renderHint() string {
	w := m.width
	if w < 8 {
		w = 8
	}
	divider := hintDividerStyle.Render(strings.Repeat("─", w))
	var hint string

	switch m.active {
	case screenContacts:
		if m.contacts.compact {
			hint = hintKeyStyle.Render("esc") + " " + hintLabelStyle.Render("go back") +
				"  " + hintKeyStyle.Render("⌥+esc") + " " + hintLabelStyle.Render("clear session")
		} else {
			hint = hintKeyStyle.Render("esc") + " " + hintLabelStyle.Render("close") +
				"  " + hintKeyStyle.Render("⌥+esc") + " " + hintLabelStyle.Render("clear session")
		}
	case screenChat:
		hint = hintKeyStyle.Render("esc") + " " + hintLabelStyle.Render("go back") +
			"  " + hintKeyStyle.Render("⌥+esc") + " " + hintLabelStyle.Render("clear session")
	default:
		return ""
	}

	return "\n" + divider + "\n" + hint + "\n"
}

func (m App) renderStatusBar() string {
	if m.connState == connDisconnected {
		color := reconnectPalette[m.tickFrame]
		return lipgloss.NewStyle().Foreground(color).Render("Reconnecting...")
	}
	return statusConnectedStyle.Render("")
}

// waitForEvent returns a Cmd that blocks until the next event on client.Events.
func waitForEvent(c *whatsapp.Client) tea.Cmd {
	return func() tea.Msg {
		return WaitForEventMsg{Event: <-c.Events}
	}
}

// animTick fires after 100ms to advance the reconnect animation.
func animTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
