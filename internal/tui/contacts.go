package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ana-rodrigues/wap/internal/whatsapp"
)

// ContactSelectedMsg is dispatched to app.go when the user picks a contact.
type ContactSelectedMsg struct {
	Contact whatsapp.Contact
}

var (
	headingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Bold(true)
	dividerStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#004D20"))
	sectionStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#004D20")).Bold(true)
	contactNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	previewStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	selectedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))
	showAllStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
)

// --- list item types ---

type contactItem struct{ contact whatsapp.Contact }

func (i contactItem) FilterValue() string { return i.contact.DisplayName }
func (i contactItem) Title() string       { return i.contact.DisplayName }
func (i contactItem) Description() string { return i.contact.LastMessage }

type sectionHeader struct{ label string }

func (h sectionHeader) FilterValue() string { return "" }
func (h sectionHeader) Title() string       { return h.label }
func (h sectionHeader) Description() string { return "" }

type showAllItem struct{}

func (i showAllItem) FilterValue() string { return "" }
func (i showAllItem) Title() string       { return "All contacts" }
func (i showAllItem) Description() string { return "" }

// --- custom delegate ---

type contactDelegate struct {
	width   int
	compact bool // true = full directory view: single line, no preview, no spacing
}

func (d contactDelegate) Height() int {
	if d.compact {
		return 1
	}
	return 2
}

func (d contactDelegate) Spacing() int {
	if d.compact {
		return 0
	}
	return 1
}

func (d contactDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d contactDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	selected := index == m.Index()

	switch v := item.(type) {
	case sectionHeader:
		fmt.Fprintln(w, "  "+sectionStyle.Render(strings.ToUpper(v.label)))
		if !d.compact {
			fmt.Fprintln(w, "")
		}

	case contactItem:
		if d.compact {
			if selected {
				fmt.Fprintln(w, selectedStyle.Render("› "+v.contact.DisplayName))
			} else {
				fmt.Fprintln(w, "  "+contactNameStyle.Render(v.contact.DisplayName))
			}
		} else {
			ts := formatTimestamp(v.contact.LastSeen)
			var timestamp string
			if ts != "" {
				timestamp = previewStyle.Render(" [" + ts + "]")
			}
			var nameLine string
			if selected {
				nameLine = selectedStyle.Render("› "+v.contact.DisplayName) + timestamp
			} else {
				nameLine = "  " + contactNameStyle.Render(v.contact.DisplayName) + timestamp
			}
			fmt.Fprintln(w, nameLine)
			fmt.Fprintln(w, "  "+previewStyle.Render(truncate(v.contact.LastMessage, 60)))
		}

	case showAllItem:
		var label string
		if selected {
			label = selectedStyle.Render("› All contacts →")
		} else {
			label = "  " + showAllStyle.Render("All contacts →")
		}
		fmt.Fprintln(w, label)
		if !d.compact {
			fmt.Fprintln(w, "")
		}
	}
}

// --- ContactsScreen ---

type ContactsScreen struct {
	list    list.Model
	spinner spinner.Model
	syncing bool
	compact bool
	width   int
	height  int
}

func NewContactsScreen(width, height int) ContactsScreen {
	return newContactsScreen(width, height, false)
}

func NewCompactContactsScreen(width, height int) ContactsScreen {
	return newContactsScreen(width, height, true)
}

func newContactsScreen(width, height int, compact bool) ContactsScreen {
	h := height - 6 // 1 heading + 1 separator + 3 hint bar + 1 status
	if h < 1 {
		h = 1
	}
	l := list.New(nil, contactDelegate{width: width, compact: compact}, width, h)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))

	return ContactsScreen{list: l, spinner: s, syncing: true, compact: compact, width: width, height: height}
}

func (m ContactsScreen) Populate(recents, all []whatsapp.Contact) ContactsScreen {
	// Stop the spinner once Populate is called.
	// If we have no recents yet (e.g., reconnect without HistorySync), show an empty list
	// so the user can still interact (open command bar, logout, quit, etc.).
	m.list.SetItems(buildItems(recents, all))
	m.syncing = false
	return m
}

func (m ContactsScreen) Init() tea.Cmd {
	if m.syncing {
		return m.spinner.Tick
	}
	return nil
}

func (m ContactsScreen) Update(msg tea.Msg) (ContactsScreen, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.syncing {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		// Don't handle contact selection while loading, but let keys bubble up
		// to app.go so global shortcuts (Shift+Esc, Ctrl+Q) still work
		if !m.syncing && msg.Type == tea.KeyEnter {
			switch item := m.list.SelectedItem().(type) {
			case contactItem:
				return m, func() tea.Msg { return ContactSelectedMsg{Contact: item.contact} }
			case showAllItem:
				return m, func() tea.Msg { return ShowAllContactsMsg{} }
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 6
		if h < 1 {
			h = 1
		}
		m.list.SetDelegate(contactDelegate{width: msg.Width, compact: m.compact})
		m.list.SetSize(msg.Width, h)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ContactsScreen) View() string {
	if m.compact {
		heading := headingStyle.Render("  ALL CONTACTS")
		if m.syncing {
			return heading + "\n\n  " + m.spinner.View() + previewStyle.Render(" Loading contacts...")
		}
		return heading + "\n" + m.list.View()
	}
	heading := headingStyle.Render("  RECENT CHATS")
	if m.syncing {
		return heading + "\n\n  " + m.spinner.View() + previewStyle.Render(" Loading chats...")
	}
	return heading + "\n" + m.list.View()
}

// --- helpers ---

func buildItems(recents, all []whatsapp.Contact) []list.Item {
	showHeaders := len(recents) > 0 && len(all) > 0
	items := make([]list.Item, 0, len(recents)+len(all)+3)

	if len(recents) > 0 {
		if showHeaders {
			items = append(items, sectionHeader{"recents"})
		}
		for _, c := range recents {
			items = append(items, contactItem{c})
		}
	}

	if len(all) > 0 {
		if showHeaders {
			items = append(items, sectionHeader{"all contacts"})
		}
		for _, c := range all {
			items = append(items, contactItem{c})
		}
	} else {
		items = append(items, showAllItem{})
	}

	return items
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := time.Now()
	y, m, d := t.Date()
	ny, nm, nd := now.Date()

	switch {
	case y == ny && m == nm && d == nd:
		return "Today at " + t.Format("15:04")
	case y == ny && m == nm && d == nd-1:
		return "Yesterday at " + t.Format("15:04")
	default:
		return t.Format("Mon Jan 2 at 15:04")
	}
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
