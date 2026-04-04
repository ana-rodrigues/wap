# WhatsApp CLI (`wap`) Design Spec

**Goal:** A lightweight terminal application that lets the user authenticate with WhatsApp, select a contact, and focus on a single conversation at a time.

**Architecture:** A Bubble Tea TUI with a global command bar, backed by a whatsmeow client wrapper that communicates via Go channels. Screens are managed as a state machine in a top-level app model.

**Tech Stack:** Go, [whatsmeow](https://github.com/tulir/whatsmeow), [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Bubbles](https://github.com/charmbracelet/bubbles), [Lipgloss](https://github.com/charmbracelet/lipgloss), SQLite (via whatsmeow's sqlstore), qrterminal.

---

## Implementation Status

### ✅ Completed

- **Auth screen** with QR code rendering, dynamic countdown timer (20s refresh cycle), instructions inside the bordered box
- **Recent Chats screen** with header + divider, recent chats ordered by last activity, message previews, unread indicators
- **All Contacts screen** (compact view) with header + divider, paginator with white active dot + spacing between bullets, search input for filtering, arrow key browsing with selection indicator (`›`)
- **Chat detail screen** with message history, real-time message delivery, optimistic send with `[!]` failed indicator, word wrapping (ANSI-aware), sender group spacing, viewport scrolling
- **Command bar** with `/all`, `/logout`, `/quit` commands
- **Connection state indicator** with animated reconnect pulse
- **Emoji handling** with `--no-emoji` flag
- **Message `IsFromMe` flag** for consistent styling of user messages across all WhatsApp channels (Web, phone, etc.)
- **Recent chats persistence** via SQLite (survives restarts)
- **History sync** from WhatsApp server on connect
- **Auth screen countdown** shows `0s` immediately on first render — the `TickMsg` from `AuthScreen.Init()` needs to be wired into `App.Init()` (currently only `waitForEvent` and `contacts.Init()` are batched there)

### 🔧 Known Issues / Remaining Work

- **QR code size** — cannot be reduced further with `qrterminal` library; the QR code size is determined by the data encoded and error correction level (currently `L` for smallest)
- **Recent Chats header** — the "RECENT CHATS" header + divider is rendered but the list height calculation may need fine-tuning if the header scrolls off screen
- **Chat input wrapping** — input text wraps via `softWrap()` but the cursor position may not track correctly on wrapped lines (limitation of `bubbles/textinput` which is single-line)
- **Contact display names** — some contacts show as JID (`351...@s.whatsapp.net`) instead of display name if the contact hasn't synced yet

---

## Screen State Machine

```
Auth ──(QR scanned / session loaded)──► ContactList ──(Enter)──► Chat
                                              ▲                     │
                                              └────────(Esc)────────┘

Any screen ──(/ key)──► CommandBar overlay
                              │
                    (/logout + Enter)
                              │
                              ▼
                           Auth (QR)
```

- **Auth → ContactList:** triggered when whatsmeow fires a `Connected` event after QR scan, or immediately on startup if `session.db` exists.
- **ContactList → Chat:** user presses `Enter` on a contact.
- **Chat → ContactList:** user presses `Esc`.
- **Any → Auth:** user types `/logout` in the command bar.

Only one conversation can be open at a time by design.

---

## Components & Files

| File | Responsibility |
|------|---------------|
| `main.go` | Entry point. Initialises whatsmeow client and starts Bubble Tea program. |
| `internal/whatsapp/client.go` | Wraps whatsmeow. Exposes typed Go channels for events (messages, connection state, QR codes). Handles auth, contact fetch, send, logout. `Message` struct includes `IsFromMe` flag for consistent sender identification. |
| `internal/tui/app.go` | Top-level Bubble Tea model. Owns active screen, command bar state, connection indicator animation, and screen transitions. |
| `internal/tui/auth.go` | Auth screen. Renders QR code via qrterminal inside a bordered box with instructions and countdown timer. Uses `tea.Tick` for 1-second countdown updates. `SetQR()` resets the countdown when a new QR code arrives. |
| `internal/tui/contacts.go` | ContactList screen. Two modes: **default** (recent chats) and **compact** (all contacts with search + paginator). Both have header + divider. |
| `internal/tui/chat.go` | Chat screen. `bubbles/viewport` for scrollable history, `bubbles/textinput` for compose. ANSI-aware word wrapping via `softWrap()`. Messages grouped by sender with blank line separators. |
| `internal/tui/commandbar.go` | Global command bar overlay. Activated by `/` from any screen. Commands: `/all` (show all contacts), `/logout`, `/quit`. |
| `internal/emoji/mapper.go` | Fallback emoji table. Maps Unicode runes without terminal glyphs to `:emoji_name:` strings. |

---

## WhatsApp Client (`internal/whatsapp/client.go`)

Wraps whatsmeow and exposes a clean interface to the TUI layer:

```go
type Client struct {
    Events    <-chan Event   // incoming: messages, connection state, QR codes
}

type Message struct {
    ID         string
    Timestamp  time.Time
    ChatJID    string
    SenderJID  string
    SenderName string
    Body       string
    Failed     bool
    Pending    bool       // optimistic messages awaiting server confirmation
    IsFromMe   bool      // true if message is from the user (sent via any channel)
    MediaType  string    // "image", "voice message", "sticker", etc.
}

func (c *Client) Connect() error
func (c *Client) RecentChats(limit int) []Contact
func (c *Client) Contacts() []Contact
func (c *Client) SendText(jid string, text string) error
func (c *Client) Logout() error
func (c *Client) SelfJID() string
func (c *Client) MarkRead(jid string)
func (c *Client) GetMessageHistory(jid string) []Message
func (c *Client) SyncMessages(jid string, msgs []Message)
```

The TUI never imports whatsmeow directly — it only talks to this interface.

**Note on phone availability:** whatsmeow uses WhatsApp's multi-device protocol. After the initial QR pairing, the CLI has its own encryption keys and communicates directly with WhatsApp servers — the phone does not need to be online.

---

## TUI Screens

### Auth Screen
- Renders the QR code using `qrterminal` with half-block mode, low error correction (`L`), and no quiet zone for compact rendering.
- QR code, instructions (3 steps), and countdown timer are all inside a single bordered box (`RoundedBorder`, `#004D20` border colour, `Padding(0, 1, 0, 1)`).
- Countdown timer displays `Code refreshes in Xs` and counts down from 20 to 0 using `tea.Tick` (1-second interval).
- `SetQR()` resets `refreshedAt` to `time.Now()` when a new QR code arrives from the WhatsApp server, restarting the countdown.
- On `EventConnected`, `app.go` transitions to ContactList.

### ContactList Screen (Recent Chats — default view)
- Header: "RECENT CHATS" with a divider line below.
- Uses `bubbles/list` showing the 5 most recent chats ordered by last activity.
- Each item shows: display name, timestamp, last message preview (truncated), unread indicator.
- `Enter` on a contact opens Chat. `Esc` quits the app.
- Real-time updates: when new messages arrive, the list refreshes automatically.
- "All contacts →" item at the bottom navigates to compact view (also accessible via `/all` command).

### ContactList Screen (All Contacts — compact view)
- Header: "ALL CONTACTS" with a divider line below.
- Single-line items (height=1, spacing=0) for compact display.
- **Selection indicator**: `›` prefix on selected item, bright green.
- **Paginator**: white active dot (`●`), gray inactive dot (`○`), with spacing between bullets.
- **Search input** at the bottom (below a divider): filters contacts by display name in real-time.
- Arrow keys browse the list. `Enter` opens Chat. `Esc` returns to Recent Chats.

### Chat Screen
- **Header**: contact display name + divider.
- **Viewport** (`bubbles/viewport`): scrollable message history.
- **Message rendering** (`renderMessage()`):
  - Format: `HH:MM You: message text` or `HH:MM SenderName: message text`
  - No indentation — all messages left-aligned.
  - Word wrapping: plain text body is wrapped via `softWrap()` *before* applying ANSI styles, so width calculation uses visible characters only. Continuation lines are indented to align with the message text start.
  - Sender grouping: a blank line is inserted between message groups from different senders (tracked via `IsFromMe` flag).
  - Failed sends: `[!]` prefix in red.
  - Media: `[image]`, `[voice message]`, etc. in gray italic.
- **Input** (`bubbles/textinput`): single-line compose with `> ` prompt. `Enter` sends. Text wraps visually via `softWrap()`.
- **Dividers**: above input and below header.
- `Esc` returns to ContactList; messages are synced back to the client via `SyncMessages()`.

### Command Bar (`/` overlay)
- Activated by pressing `/` from any screen.
- A single-line input appears at the bottom of the terminal.
- Recognised commands:
  - `/all` — opens All Contacts view
  - `/logout` — calls `client.Logout()`, deletes session, transitions to Auth
  - `/quit` — quits the app (preserves session)
- Unknown commands show a brief inline error ("Unknown command").
- `Esc` dismisses the command bar without action.

---

## Session & Storage

- Session data stored in `session.db` (SQLite, managed by whatsmeow's `sqlstore`) in the current working directory.
- Recent chats are persisted in a separate SQLite table (`recent_chats`) so they survive restarts.
- **Startup logic:**
  - `session.db` exists → connect silently, skip Auth screen.
  - `session.db` missing → show Auth/QR screen.
- **Logout:** `client.Logout()` signals the WhatsApp server, then session is deleted. App transitions to Auth for a fresh QR scan.

---

## Connection State Indicator

- A status bar line is always visible at the bottom of the screen (above the command bar when active).
- Normal state: blank / subtle "Connected" in muted colour.
- On `EventDisconnected`: displays "Reconnecting..." with an animated colour pulse.
  - Implemented via `tea.Tick` (every 100ms) advancing a frame index in `app.go`.
  - `lipgloss` cycles through an amber/orange palette per frame.
  - Tick is only active when connection state is `disconnected`; zero overhead otherwise.
- On reconnect (`EventConnected`): animation stops, indicator clears.

---

## Keyboard Shortcuts

| Key | Context | Action |
|-----|---------|--------|
| `Enter` | ContactList | Open selected contact's chat |
| `Esc` | Recent Chats | Quit (preserve session) |
| `Esc` | All Contacts | Return to Recent Chats |
| `Esc` | Chat | Return to ContactList |
| `⌥+Esc` | Any | Logout and quit (clear session) |
| `Ctrl+C` | Any | Quit (preserve session) |
| `/` | Any | Open command bar |
| `↑/↓` | ContactList | Navigate contacts |
| `Enter` | Chat (input) | Send message |

---

## Emoji Handling

- Emoji are passed through to the terminal as-is.
- A `--no-emoji` flag forces all emoji to their `:shortcode:` text equivalents via `internal/emoji/mapper.go`.

---

## Error Handling

| Scenario | Behaviour |
|----------|-----------|
| QR code expires | whatsmeow emits new QR event; Auth screen re-renders, countdown resets |
| Connection loss | Animated "Reconnecting..." indicator; whatsmeow retries automatically |
| Send failure | Message displayed with `[!]` prefix in chat |
| Empty contact list on first login | "Syncing contacts..." with spinner shown until `EventContactsReady` |
| Unsupported message type | Descriptive placeholder: `[image]`, `[voice message]`, `[sticker]`, etc. |
| Unknown `/command` | Inline error in command bar: "Unknown command" |

---

## Visual Design

### Palette

Terminal background is the user's default (no override). All chrome uses a small set of Lipgloss colours:

| Role | Value | Usage |
|------|-------|-------|
| White | terminal default | Message body — primary content, never overridden |
| Accent | `#00E676` (bright green) | "You" sender name, active selection, section labels, command bar `/`, input prompt |
| Light green | `#81C784` | Other sender names |
| Gray | `#888888` | Timestamps, subtitles, placeholders, media labels, dim text |
| Dark gray | `#555555` | Dividers, inactive paginator dots |
| Subtle | `#004D20` (dark green) | QR code box border |
| Error | `#FF5252` (red) | `[!]` failed send |

### Message Rendering

Plain text lines — no bubbles, no indentation. All messages left-aligned:

```
10:42 You: hey, are you around?
10:43 Ana: yes! one sec
10:43 Ana: [image]
```

- Timestamp: gray `#888888`
- Sender "You:": bright green `#00E676`, bold
- Sender others: light green `#81C784`
- Message body: terminal default (white)
- Long messages wrap at word boundaries; continuation lines indented to align with message text
- Blank line between message groups from different senders
- `[!]` failed sends: error red prefix
- `[image]` / `[voice message]` placeholders: gray, italicised

### Screen Layout (all screens)

All screens follow a consistent layout pattern:
- **Header**: title text (e.g., "RECENT CHATS", "ALL CONTACTS", contact name)
- **Divider**: full-width `─` line in dark gray below header
- **Content area**: list or viewport
- **Footer divider + input** (where applicable): divider + text input
- **Hint bar**: keyboard shortcuts in muted text

### ContactList

```
  RECENT CHATS
─────────────────────────────
› Ana Rodrigues    [Today at 10:42]
  hey, are you around?

  Work Group       [Yesterday at 15:30]
  the doc is ready

  All contacts →
```

### All Contacts (compact)

```
  ALL CONTACTS
─────────────────────────────
› Bruno Silva
  Carla Mendes
  ...
                 ● ○ ○
─────────────────────────────
Search contacts...
```

### Command Bar

Appears above the status bar when active. Single line with a bright green `/` prefix and white input text.

```
/ logout█
```

---

## Out of Scope (v1)

- Media download and open (planned v2)
- Group chats
- Read receipts / typing indicators
- Message search
- Notifications (desktop or sound)
- Multiple simultaneous conversations
- Custom themes / colour configuration
