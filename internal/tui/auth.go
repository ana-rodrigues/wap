package tui

import (
	"bytes"
	"fmt"
	"strings"
	"time"

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
				Padding(0, 1, 0, 1)
)

// AuthScreen is the Bubble Tea model for the QR authentication screen.
type AuthScreen struct {
	qrCode      string
	refreshedAt time.Time
}

func NewAuthScreen() AuthScreen { return AuthScreen{refreshedAt: time.Now()} }

// TickMsg is sent every second to update the countdown timer
type TickMsg struct{}

func (m AuthScreen) Init() tea.Cmd {
	return tea.Tick(1*time.Second, func(time.Time) tea.Msg {
		return TickMsg{}
	})
}

func (m AuthScreen) Update(msg tea.Msg) (AuthScreen, tea.Cmd) {
	switch msg.(type) {
	case TickMsg:
		// Re-trigger the tick for continuous updates
		return m, tea.Tick(1*time.Second, func(time.Time) tea.Msg {
			return TickMsg{}
		})
	}
	return m, nil
}

// SetQR updates the displayed QR code and resets the refresh timer.
func (m AuthScreen) SetQR(code string) AuthScreen {
	m.qrCode = code
	m.refreshedAt = time.Now()
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

	// QR code — half-block mode keeps it compact, smaller size
	var buf bytes.Buffer
	qrterminal.GenerateWithConfig(m.qrCode, qrterminal.Config{
		Level:      qrterminal.L,
		Writer:     &buf,
		HalfBlocks: true,
		BlackChar:  qrterminal.BLACK_BLACK,
		WhiteChar:  qrterminal.WHITE_WHITE,
		QuietZone:  0,
	})
	qrOutput := buf.String()
	// Trim trailing newline from QR code
	qrOutput = strings.TrimSuffix(qrOutput, "\n")

	// Build content inside the box: QR code + instructions + countdown
	var boxContent strings.Builder
	boxContent.WriteString(qrOutput)
	boxContent.WriteString("\n\n")

	// Instructions
	boxContent.WriteString(authStepStyle.Render("1. Open WhatsApp on your phone"))
	boxContent.WriteString("\n")
	boxContent.WriteString(authStepStyle.Render("2. Go to Settings → Linked Devices"))
	boxContent.WriteString("\n")
	boxContent.WriteString(authStepStyle.Render("3. Tap \"Link a Device\" and scan above"))
	boxContent.WriteString("\n\n")

	// Countdown timer
	const refreshInterval = 20 * time.Second
	elapsed := time.Since(m.refreshedAt)
	remaining := refreshInterval - elapsed
	if remaining < 0 {
		remaining = 0
	}
	countdownSecs := int(remaining.Seconds())
	boxContent.WriteString(authDimStyle.Render(fmt.Sprintf("Code refreshes in %ds", countdownSecs)))

	// Render the entire box with border
	sb.WriteString(authBorderStyle.Render(boxContent.String()))

	return sb.String()
}
