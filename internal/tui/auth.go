package tui

import (
	"bytes"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mdp/qrterminal/v3"
)

var (
	authTitleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676")).Bold(true)
	authSubtitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	authStepStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	authDimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	authBorderStyle   = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#004D20")).
				Padding(0, 1)
)

// AuthScreen is the Bubble Tea model for the QR authentication screen.
type AuthScreen struct {
	qrCode string
}

func NewAuthScreen() AuthScreen { return AuthScreen{} }

func (m AuthScreen) Init() tea.Cmd { return nil }

func (m AuthScreen) Update(msg tea.Msg) (AuthScreen, tea.Cmd) { return m, nil }

// SetQR updates the displayed QR code.
func (m AuthScreen) SetQR(code string) AuthScreen {
	m.qrCode = code
	return m
}

func (m AuthScreen) View() string {
	var sb strings.Builder

	// Header
	sb.WriteString(authTitleStyle.Render("wap"))
	sb.WriteString("\n")
	sb.WriteString(authSubtitleStyle.Render("WhatsApp for your terminal"))
	sb.WriteString("\n\n")

	if m.qrCode == "" {
		sb.WriteString(authDimStyle.Render("Connecting to WhatsApp..."))
		return sb.String()
	}

	// QR code — half-block mode keeps it compact
	var buf bytes.Buffer
	qrterminal.GenerateWithConfig(m.qrCode, qrterminal.Config{
		Level:      qrterminal.L,
		Writer:     &buf,
		HalfBlocks: true,
		BlackChar:  qrterminal.BLACK_BLACK,
		WhiteChar:  qrterminal.WHITE_WHITE,
		QuietZone:  1,
	})
	sb.WriteString(authBorderStyle.Render(buf.String()))
	sb.WriteString("\n\n")

	// Instructions
	sb.WriteString(authStepStyle.Render("  1. Open WhatsApp on your phone"))
	sb.WriteString("\n")
	sb.WriteString(authStepStyle.Render("  2. Go to Settings → Linked Devices"))
	sb.WriteString("\n")
	sb.WriteString(authStepStyle.Render("  3. Tap \"Link a Device\" and scan the code above"))
	sb.WriteString("\n\n")
	sb.WriteString(authDimStyle.Render("  Code refreshes automatically every 20s"))

	return sb.String()
}
