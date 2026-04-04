package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ana-rodrigues/wap/internal/emoji"
	"github.com/ana-rodrigues/wap/internal/whatsapp"
)

var (
	tsStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	senderYouStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676")).Bold(true) // Bright green for "You"
	senderOtherStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#81C784"))            // Lighter green for others
	msgBodyStyle     = lipgloss.NewStyle()                                                  // terminal default (white)
	msgMediaStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Italic(true)
	msgFailStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5252"))

	inputPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))
)

const (
	tsWidth       = 5  // "HH:MM"
	senderWidth   = 12 // padded sender name column
	userMsgIndent = 3  // spaces to indent user's messages
)

// WaitForEventMsg is the message returned by the waitForEvent Cmd.
type WaitForEventMsg struct {
	Event whatsapp.Event
}

// ChatScreen is the Bubble Tea model for a single active conversation.
type ChatScreen struct {
	contact  whatsapp.Contact
	selfJID  string
	messages []whatsapp.Message
	viewport viewport.Model
	input    textinput.Model
	noEmoji  bool
	width    int
	height   int
	ready    bool
}

// Messages returns the current message list (for syncing back to client on exit).
func (m ChatScreen) Messages() []whatsapp.Message {
	return m.messages
}

// NewChatScreen creates a chat screen for the given contact.
func NewChatScreen(contact whatsapp.Contact, client *whatsapp.Client, noEmoji bool, width, height int) ChatScreen {
	ti := textinput.New()
	ti.Placeholder = "Message..."
	ti.CharLimit = 4096
	ti.Focus()

	vp := viewport.New(width, chatViewportHeight(height))

	// Load message history from client
	messages := client.GetMessageHistory(contact.JID)

	m := ChatScreen{
		contact:  contact,
		selfJID:  client.SelfJID(),
		messages: messages,
		noEmoji:  noEmoji,
		viewport: vp,
		input:    ti,
		width:    width,
		height:   height,
		ready:    true,
	}

	// Render initial messages and scroll to bottom
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()

	return m
}

func (m ChatScreen) Init() tea.Cmd { return nil }

func (m ChatScreen) Update(msg tea.Msg) (ChatScreen, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = chatViewportHeight(msg.Height)
		m.viewport.SetContent(m.renderMessages())

	case WaitForEventMsg:
		if msg.Event.Kind == whatsapp.EventMessage {
			if ev, ok := msg.Event.Payload.(whatsapp.Message); ok {
				// Client normalizes all JIDs to phone format, so exact match works
				if chatJID(ev.ChatJID) == chatJID(m.contact.JID) {
					// Check if this is a server confirmation of an optimistic message
					replaced := false
					if ev.SenderJID == m.selfJID || chatJID(ev.SenderJID) == chatJID(m.selfJID) {
						// This is our sent message — find the pending optimistic copy
						for i := len(m.messages) - 1; i >= 0; i-- {
							if m.messages[i].Pending && m.messages[i].Body == ev.Body {
								m.messages[i] = ev
								replaced = true
								break
							}
						}
					}

					if !replaced {
						m.messages = append(m.messages, ev)
					}

					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
				}
			}
		}

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text != "" {
				m.input.SetValue("")
				return m, sendMsg(text)
			}
		}
	}

	var vpCmd, tiCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	m.input, tiCmd = m.input.Update(msg)
	cmds = append(cmds, vpCmd, tiCmd)
	return m, tea.Batch(cmds...)
}

// SendTextMsg carries the text to send up to app.go, which has access to the client.
type SendTextMsg struct{ Text string }

func sendMsg(text string) tea.Cmd {
	return func() tea.Msg { return SendTextMsg{Text: text} }
}

// generateTempID creates a temporary message ID for optimistic rendering
func generateTempID() string {
	return fmt.Sprintf("temp_%d", time.Now().UnixNano())
}

// AddSentMessage adds an optimistically-rendered outgoing message with pending state.
func (m ChatScreen) AddSentMessage(text string) ChatScreen {
	m.messages = append(m.messages, whatsapp.Message{
		ID:         generateTempID(),
		Timestamp:  time.Now(),
		ChatJID:    m.contact.JID,
		SenderJID:  m.selfJID,
		SenderName: "You",
		Body:       text,
		Pending:    true, // Mark as pending until server confirms
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m
}

// AddFailedMessage marks the last pending message as failed.
func (m ChatScreen) AddFailedMessage(text string) ChatScreen {
	m.messages = append(m.messages, whatsapp.Message{
		ID:         generateTempID(),
		Timestamp:  time.Now(),
		ChatJID:    m.contact.JID,
		SenderJID:  m.selfJID,
		SenderName: "You",
		Body:       text,
		Failed:     true,
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m
}

func (m ChatScreen) View() string {
	prompt := inputPromptStyle.Render("> ")
	inputLine := prompt + m.input.View()
	return m.viewport.View() + "\n" + inputLine
}

// renderMessages builds the full viewport content string from m.messages.
func (m ChatScreen) renderMessages() string {
	if len(m.messages) == 0 {
		return msgMediaStyle.Render("  No messages yet.")
	}

	var sb strings.Builder
	for _, msg := range m.messages {
		sb.WriteString(renderMessage(msg, m.selfJID, m.noEmoji))
		sb.WriteRune('\n')
	}
	return sb.String()
}

func renderMessage(msg whatsapp.Message, selfJID string, noEmoji bool) string {
	ts := tsStyle.Render(msg.Timestamp.Format("15:04"))
	isMe := chatJID(msg.SenderJID) == chatJID(selfJID)

	var body string
	switch {
	case msg.Failed:
		prefix := msgFailStyle.Render("[!] ")
		body = prefix + msgBodyStyle.Render(emoji.MapString(msg.Body, noEmoji))
	case msg.MediaType != "":
		body = msgMediaStyle.Render("[" + msg.MediaType + "]")
	default:
		body = msgBodyStyle.Render(emoji.MapString(msg.Body, noEmoji))
	}

	if isMe {
		// Your messages: indented with bright green "You:" prefix
		// Format:    [15:04] You: message text
		indent := strings.Repeat(" ", userMsgIndent)
		sender := senderYouStyle.Render("You:")
		return indent + ts + " " + sender + " " + body
	} else {
		// Others' messages: left-aligned with lighter green sender name
		// Format: [15:04] SenderName: message text
		senderName := msg.SenderName
		if senderName == "" {
			// Fallback to JID if name not available
			senderName = chatJID(msg.SenderJID)
		}
		sender := senderOtherStyle.Render(senderName + ":")
		return ts + " " + sender + " " + body
	}
}

// chatViewportHeight calculates the viewport height leaving room for input + hint bar + status bar.
func chatViewportHeight(total int) int {
	const inputLines = 1
	const hintBar = 3 // \n + divider + \n + hint + \n
	const statusBar = 1
	h := total - inputLines - hintBar - statusBar
	if h < 1 {
		return 1
	}
	return h
}

// chatJID strips the device suffix from a JID for comparison (e.g. "user@s.whatsapp.net:0" → "user@s.whatsapp.net").
func chatJID(jid string) string {
	if idx := strings.IndexByte(jid, ':'); idx != -1 {
		return jid[:idx]
	}
	return jid
}
