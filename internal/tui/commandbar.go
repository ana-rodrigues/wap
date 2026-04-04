package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ShowAllContactsMsg is dispatched when the user runs /contacts.
type ShowAllContactsMsg struct{}

// QuitAppMsg is dispatched when the user runs /quit.
type QuitAppMsg struct{}

// LogoutMsg is dispatched when the user runs /logout.
type LogoutMsg struct{}

// DismissCommandBarMsg is dispatched when the command bar is dismissed without action.
type DismissCommandBarMsg struct{}

var (
	cmdBarPrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676")).Bold(true)
	cmdBarErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5252"))
)

// CommandBar is a Bubble Tea model for the global command overlay.
type CommandBar struct {
	input textinput.Model
	err   string
}

// NewCommandBar creates a ready-to-use CommandBar model.
func NewCommandBar() CommandBar {
	ti := textinput.New()
	ti.Placeholder = "command..."
	ti.CharLimit = 64
	ti.Focus()
	return CommandBar{input: ti}
}

func (m CommandBar) Init() tea.Cmd {
	return textinput.Blink
}

func (m CommandBar) Update(msg tea.Msg) (CommandBar, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return m, func() tea.Msg { return DismissCommandBarMsg{} }

		case tea.KeyEnter:
			cmd := strings.TrimSpace(m.input.Value())
			switch cmd {
			case "contacts", "/contacts":
				return m, func() tea.Msg { return ShowAllContactsMsg{} }
			case "quit", "/quit":
				return m, func() tea.Msg { return QuitAppMsg{} }
			case "logout", "/logout":
				return m, func() tea.Msg { return LogoutMsg{} }
			default:
				m.err = "Unknown command"
				return m, nil
			}
		}
	}

	var tiCmd tea.Cmd
	m.input, tiCmd = m.input.Update(msg)
	m.err = "" // clear error on any typing
	return m, tiCmd
}

// View renders the command bar as a single line for embedding in app.go's View.
func (m CommandBar) View() string {
	prefix := cmdBarPrefixStyle.Render("/ ")
	line := prefix + m.input.View()
	if m.err != "" {
		line += "  " + cmdBarErrorStyle.Render(m.err)
	}
	return line
}
