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
	headerDivider    = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
)

const (
	tsWidth     = 5  // "HH:MM"
	senderWidth = 12 // padded sender name column
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
	ti.Placeholder = "Your message..."
	ti.CharLimit = 4096
	ti.Focus()

	vp := viewport.New(width, chatViewportHeight(height))

	// Load message history from client, resolving any missing sender names.
	messages := client.GetMessageHistory(contact.JID)
	for i := range messages {
		if messages[i].SenderName == "" {
			if messages[i].IsFromMe {
				messages[i].SenderName = "You"
			} else {
				messages[i].SenderName = client.ContactName(messages[i].SenderJID)
			}
		}
	}

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
		IsFromMe:   true,
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
		IsFromMe:   true,
		Failed:     true,
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m
}

func (m ChatScreen) View() string {
	// Build header with contact name and divider
	header := chatHeaderStyle.Render("  " + m.contact.DisplayName)
	divider := headerDivider.Render(strings.Repeat("─", m.width))

	// Input box with divider above it
	inputDivider := headerDivider.Render(strings.Repeat("─", m.width))
	inputText := strings.TrimSpace(m.input.View())

	// Word wrap the input text to fit within terminal width
	wrappedInput := softWrap(inputText, m.width)

	// Leading newline to match spacing with Recent Chats header
	return "\n" + header + "\n" + divider + "\n" + m.viewport.View() + "\n" + inputDivider + "\n" + wrappedInput
}

// renderMessages builds the full viewport content string from m.messages.
func (m ChatScreen) renderMessages() string {
	if len(m.messages) == 0 {
		return "\n" + msgMediaStyle.Render("  No messages yet.")
	}

	var sb strings.Builder
	sb.WriteString(msgMediaStyle.Render("— showing last " + fmt.Sprintf("%d", len(m.messages)) + " messages —"))
	sb.WriteRune('\n')
	for i, msg := range m.messages {
		sb.WriteRune('\n')
		sb.WriteString(renderMessage(msg, m.selfJID, m.noEmoji, m.width))
		if i < len(m.messages)-1 {
			sb.WriteRune('\n')
		}
	}
	return sb.String()
}

func renderMessage(msg whatsapp.Message, selfJID string, noEmoji bool, width int) string {
	tsText := msg.Timestamp.Format("15:04")

	var bodyText string
	switch {
	case msg.Failed:
		bodyText = "[!] " + emoji.MapString(msg.Body, noEmoji)
	case msg.MediaType != "":
		bodyText = "[" + msg.MediaType + "]"
	default:
		bodyText = emoji.MapString(msg.Body, noEmoji)
	}

	if msg.IsFromMe {
		header := tsStyle.Render(tsText) + " " + senderYouStyle.Render("You:")
		lines := strings.Split(softWrap(bodyText, width), "\n")
		var sb strings.Builder
		sb.WriteString(header)
		for _, line := range lines {
			sb.WriteRune('\n')
			sb.WriteString(styleMsgBody(msg, line, noEmoji))
		}
		return sb.String()
	}

	senderName := msg.SenderName
	if senderName == "" {
		senderName = chatJID(msg.SenderJID)
	}
	header := tsStyle.Render(tsText) + " " + senderOtherStyle.Render(senderName+":")
	lines := strings.Split(softWrap(bodyText, width), "\n")
	var sb strings.Builder
	sb.WriteString(header)
	for _, line := range lines {
		sb.WriteRune('\n')
		sb.WriteString(styleMsgBody(msg, line, noEmoji))
	}
	return sb.String()
}

// styleMsgBody applies the correct style to a message body line
func styleMsgBody(msg whatsapp.Message, line string, noEmoji bool) string {
	switch {
	case msg.Failed:
		return msgFailStyle.Render(line)
	case msg.MediaType != "":
		return msgMediaStyle.Render(line)
	default:
		return msgBodyStyle.Render(line)
	}
}

// softWrap wraps text at word boundaries to fit within maxWidth visible characters
func softWrap(text string, maxWidth int) string {
	if maxWidth <= 0 || len(text) <= maxWidth {
		return text
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return text
	}

	var lines []string
	currentLine := words[0]

	for _, word := range words[1:] {
		if len(currentLine)+1+len(word) <= maxWidth {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}
	lines = append(lines, currentLine)
	return strings.Join(lines, "\n")
}

// chatViewportHeight calculates the viewport height leaving room for header + input + hint bar + status bar.
func chatViewportHeight(total int) int {
	const leadingNewline = 1 // blank line before header to match Recent Chats
	const headerLines = 2    // contact name + divider
	const inputDivider = 1   // divider above input
	const inputLines = 1
	const hintBar = 3 // \n + divider + \n + hint + \n
	const statusBar = 1
	h := total - leadingNewline - headerLines - inputDivider - inputLines - hintBar - statusBar
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
