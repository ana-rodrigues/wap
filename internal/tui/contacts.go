package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ana-rodrigues/wap/internal/whatsapp"
)

// ContactSelectedMsg is dispatched to app.go when the user picks a contact.
type ContactSelectedMsg struct {
	Contact whatsapp.Contact
}

var (
	headingStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Bold(true)
	dividerStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#004D20"))
	sectionStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#004D20")).Bold(true)
	contactNameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	previewStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	selectedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))
	showAllStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	unreadNameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676")).Bold(true)
	unreadPreviewStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa")).Bold(true)
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

// mainHeader is the top-level header (RECENT CHATS / ALL CONTACTS) with gray color and divider
type mainHeader struct{ title string }

func (h mainHeader) FilterValue() string { return "" }
func (h mainHeader) Title() string       { return h.title }
func (h mainHeader) Description() string { return "" }

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
	// No spacing between items in any view for a more compact list
	return 0
}

func (d contactDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d contactDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	selected := index == m.Index()

	switch v := item.(type) {
	case mainHeader:
		fmt.Fprintln(w, "  "+chatHeaderStyle.Render(v.Title()))
		fmt.Fprintln(w, headerDivider.Render(strings.Repeat("─", d.width)))

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

			nameStyle := contactNameStyle
			prvStyle := previewStyle
			if v.contact.Unread {
				nameStyle = unreadNameStyle
				prvStyle = unreadPreviewStyle
				timestamp += " " + unreadNameStyle.Render("●")
			}

			var nameLine string
			if selected {
				nameLine = selectedStyle.Render("› "+v.contact.DisplayName) + timestamp
			} else {
				nameLine = "  " + nameStyle.Render(v.contact.DisplayName) + timestamp
			}
			fmt.Fprintln(w, nameLine)
			fmt.Fprintln(w, "  "+prvStyle.Render(truncate(v.contact.LastMessage, 60)))
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
	list     list.Model
	spinner  spinner.Model
	search   textinput.Model // search input for compact view
	syncing  bool
	compact  bool
	width    int
	height   int
	allItems []list.Item // original unfiltered items for searching

	// Compact view specific fields (when compact=true)
	compactContacts []whatsapp.Contact // all contacts for All Contacts view
	cursor          int                // selected index in compact view
	filtered        []whatsapp.Contact // filtered contacts when searching
}

func NewContactsScreen(width, height int) ContactsScreen {
	return newContactsScreen(width, height, false)
}

func NewCompactContactsScreen(width, height int) ContactsScreen {
	return newContactsScreen(width, height, true)
}

func newContactsScreen(width, height int, compact bool) ContactsScreen {
	// Calculate available height for the list:
	// - Header: 2 lines (heading + divider)
	// - Newlines in View(): 2 lines
	// - Footer: 5 lines (1 empty + 1 divider + 1 hint + 1 trailing newline + 1 status)
	// - Buffer: 1 line
	// Total: 10 lines to subtract
	h := height - 10
	if compact {
		h -= 5 // external header (3) + search input + divider
	}
	if h < 1 {
		h = 1
	}
	l := list.New(nil, contactDelegate{width: width, compact: compact}, width, h)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	// Style the paginator to make current page white and visible
	l.Paginator.ActiveDot = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Render("● ")
	l.Paginator.InactiveDot = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555")).Render("○ ")

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#00E676"))

	// Initialize search input for compact view
	search := textinput.New()
	search.Placeholder = "Search contacts..."
	search.CharLimit = 256
	// Don't focus search by default - let list be interactive with arrow keys
	// User can still type to search (textinput captures typing automatically)

	return ContactsScreen{list: l, spinner: s, search: search, syncing: true, compact: compact, width: width, height: height, allItems: []list.Item{}, cursor: 0}
}

func (m ContactsScreen) Populate(recents, all []whatsapp.Contact) ContactsScreen {
	// Stop the spinner once Populate is called.
	// If we have no recents yet (e.g., reconnect without HistorySync), show an empty list
	// so the user can still interact (open command bar, logout, quit, etc.).
	items := buildItems(recents, all)
	m.allItems = items
	m.list.SetItems(items)
	// Pre-select the first contact item only for Recent Chats
	// For All Contacts, start at top so first items are visible
	if recents != nil && len(items) > 0 {
		for i, item := range items {
			if _, ok := item.(contactItem); ok {
				m.list.Select(i)
				break
			}
		}
	}
	// Store contacts for compact view
	m.compactContacts = all
	m.filtered = all
	m.cursor = 0
	m.syncing = false
	return m
}

// filterItems returns items matching the search query
func (m ContactsScreen) filterItems(query string) []list.Item {
	if query == "" {
		return m.allItems
	}

	query = strings.ToLower(query)
	var filtered []list.Item

	for _, item := range m.allItems {
		if contact, ok := item.(contactItem); ok {
			if strings.Contains(strings.ToLower(contact.contact.DisplayName), query) {
				filtered = append(filtered, item)
			}
		}
	}
	return filtered
}

// filterCompact filters contacts for compact view based on search query
func (m ContactsScreen) filterCompact() {
	query := strings.ToLower(m.search.Value())
	if query == "" {
		m.filtered = m.compactContacts
		m.cursor = 0
		return
	}

	var filtered []whatsapp.Contact
	for _, c := range m.compactContacts {
		if strings.Contains(strings.ToLower(c.DisplayName), query) {
			filtered = append(filtered, c)
		}
	}
	m.filtered = filtered
	if m.cursor >= len(filtered) {
		m.cursor = 0
	}
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
		// In compact view, handle search input first
		if m.compact && !m.syncing {
			// Handle escape to go back
			if msg.Type == tea.KeyEsc {
				// Let ESC bubble up to app.go to handle navigation back
				break
			}

			// Handle Enter to select contact
			if msg.Type == tea.KeyEnter {
				contacts := m.filtered
				if len(contacts) > 0 && m.cursor >= 0 && m.cursor < len(contacts) {
					return m, func() tea.Msg { return ContactSelectedMsg{Contact: contacts[m.cursor]} }
				}
				return m, nil
			}

			// Handle arrow keys for navigation
			switch msg.Type {
			case tea.KeyUp:
				if m.cursor > 0 {
					m.cursor--
				}
				return m, nil
			case tea.KeyDown:
				contacts := m.filtered
				if m.cursor < len(contacts)-1 {
					m.cursor++
				}
				return m, nil
			case tea.KeyHome:
				m.cursor = 0
				return m, nil
			case tea.KeyEnd:
				m.cursor = len(m.filtered) - 1
				if m.cursor < 0 {
					m.cursor = 0
				}
				return m, nil
			case tea.KeyPgUp:
				m.cursor -= 10
				if m.cursor < 0 {
					m.cursor = 0
				}
				return m, nil
			case tea.KeyPgDown:
				contacts := m.filtered
				m.cursor += 10
				if m.cursor >= len(contacts) {
					m.cursor = len(contacts) - 1
					if m.cursor < 0 {
						m.cursor = 0
					}
				}
				return m, nil
			}

			// For typing keys, handle search
			if m.search.Focused() {
				var cmd tea.Cmd
				m.search, cmd = m.search.Update(msg)
				m.filterCompact()
				return m, cmd
			}

			// Focus search and handle typing
			if msg.String() != "" {
				m.search.Focus()
				var cmd tea.Cmd
				m.search, cmd = m.search.Update(msg)
				m.filterCompact()
				return m, cmd
			}

			// Let any other keys pass through
			break
		}

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
		// Reduce height to account for search input in compact view
		// Header (2) + newlines (2) + footer (5) + buffer (1) = 10
		h := msg.Height - 10
		if m.compact {
			h -= 5 // external header (3) + search input + divider
		}
		if h < 1 {
			h = 1
		}
		m.list.SetDelegate(contactDelegate{width: msg.Width, compact: m.compact})
		m.list.SetSize(msg.Width, h)
		m.search.Width = msg.Width
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ContactsScreen) View() string {
	divider := headerDivider.Render(strings.Repeat("─", m.width))

	if m.compact {
		// Header is now part of the list via mainHeader, except during loading
		if m.syncing {
			header := chatHeaderStyle.Render("  ALL CONTACTS")
			return "\n" + header + "\n" + divider + "\n\n  " + m.spinner.View() + previewStyle.Render(" Loading contacts...")
		}
		// Show search input with divider above it
		searchDivider := headerDivider.Render(strings.Repeat("─", m.width))
		searchInput := strings.TrimSuffix(m.search.View(), "\n")
		header := chatHeaderStyle.Render("  ALL CONTACTS")
		// Render contacts directly
		contactsView := m.renderCompactContacts()
		return "\n" + header + "\n" + divider + "\n" + contactsView + "\n" + searchDivider + "\n" + searchInput
	}
	// Non-compact view (Recent Chats)
	if m.syncing {
		header := chatHeaderStyle.Render("  RECENT CHATS")
		return "\n" + header + "\n" + divider + "\n\n  " + m.spinner.View() + previewStyle.Render(" Loading chats...")
	}
	// Header is now part of the list via sectionHeader, no divider needed
	return m.list.View()
}

// renderCompactContacts renders the contact list for compact view
func (m ContactsScreen) renderCompactContacts() string {
	contacts := m.filtered
	if len(contacts) == 0 {
		return "  " + previewStyle.Render("No contacts found")
	}

	// Calculate visible range based on cursor
	// Keep cursor visible in the middle when possible
	visibleHeight := m.height - 10 // Account for header, search, footer
	if visibleHeight < 5 {
		visibleHeight = 5
	}

	startIdx := 0
	if m.cursor > visibleHeight/2 {
		startIdx = m.cursor - visibleHeight/2
	}
	endIdx := startIdx + visibleHeight
	if endIdx > len(contacts) {
		endIdx = len(contacts)
		startIdx = endIdx - visibleHeight
		if startIdx < 0 {
			startIdx = 0
		}
	}

	var sb strings.Builder
	for i := startIdx; i < endIdx && i < len(contacts); i++ {
		c := contacts[i]
		if i == m.cursor {
			sb.WriteString(selectedStyle.Render("› " + c.DisplayName))
		} else {
			sb.WriteString("  " + contactNameStyle.Render(c.DisplayName))
		}
		sb.WriteRune('\n')
	}

	return sb.String()
}

func buildItems(recents, all []whatsapp.Contact) []list.Item {
	showHeaders := len(recents) > 0 && len(all) > 0
	items := make([]list.Item, 0, len(recents)+len(all)+4)

	// Always add main header as first item for Recent Chats only
	// All Contacts has header rendered in View() instead
	if recents != nil {
		items = append(items, mainHeader{"RECENT CHATS"})
	}

	if len(recents) > 0 {
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
