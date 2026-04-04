# WhatsApp CLI (`wacli`) Design Spec

**Goal:** A lightweight terminal application that lets the user authenticate with WhatsApp, select a contact, and focus on a single conversation at a time.

**Architecture:** A Bubble Tea TUI with a global command bar, backed by a whatsmeow client wrapper that communicates via Go channels. Screens are managed as a state machine in a top-level app model.

**Tech Stack:** Go, [whatsmeow](https://github.com/tulir/whatsmeow), [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Bubbles](https://github.com/charmbracelet/bubbles), [Lipgloss](https://github.com/charmbracelet/lipgloss), SQLite (via whatsmeow's sqlstore), qrterminal.

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
| `internal/whatsapp/client.go` | Wraps whatsmeow. Exposes typed Go channels for events (messages, connection state, QR codes). Handles auth, contact fetch, send, logout. |
| `internal/tui/app.go` | Top-level Bubble Tea model. Owns active screen, command bar state, connection indicator animation, and screen transitions. |
| `internal/tui/auth.go` | Auth screen. Renders QR code via qrterminal. Listens for `Connected` event to transition. |
| `internal/tui/contacts.go` | ContactList screen. Uses `bubbles/list` with built-in type-to-filter. Renders contact display names. |
| `internal/tui/chat.go` | Chat screen. `bubbles/viewport` for scrollable history, `bubbles/textinput` for compose. Renders incoming messages reactively. |
| `internal/tui/commandbar.go` | Global command bar overlay. Activated by `/` from any screen. Dispatches recognised commands to `app.go`. |
| `internal/emoji/mapper.go` | Fallback emoji table. Maps Unicode runes without terminal glyphs to `:emoji_name:` strings. |
| `store/` | SQLite session database written by whatsmeow (`~/.config/wacli/session.db`). |

---

## WhatsApp Client (`internal/whatsapp/client.go`)

Wraps whatsmeow and exposes a clean interface to the TUI layer:

```go
type Client struct {
    Events    <-chan Event   // incoming: messages, connection state, QR codes
    // internal whatsmeow client, store, etc.
}

type EventKind int
const (
    EventQRCode EventKind = iota
    EventConnected
    EventDisconnected
    EventMessage
    EventContactsReady
)

type Event struct {
    Kind    EventKind
    Payload any  // *waProto.Message, QR string, etc.
}

func (c *Client) Connect() error
func (c *Client) RecentChats(limit int) []Contact            // ordered by last activity, tracked in-memory (whatsmeow has no chat store)
func (c *Client) Contacts() []Contact                       // full contact list, alphabetical, from sqlstore contact store
func (c *Client) SendText(jid string, text string) error
func (c *Client) Logout() error  // calls whatsmeow Logout(), deletes session.db
```

The TUI never imports whatsmeow directly — it only talks to this interface.

**Note on phone availability:** whatsmeow uses WhatsApp's multi-device protocol. After the initial QR pairing, the CLI has its own encryption keys and communicates directly with WhatsApp servers — the phone does not need to be online.

---

## TUI Screens

### Auth Screen
- Renders the QR code as ASCII art using `qrterminal`.
- Displays "Scan with WhatsApp on your phone" below the code.
- whatsmeow automatically emits a new QR event when the code expires (~20s); the screen re-renders with the fresh code.
- On `EventConnected`, `app.go` transitions to ContactList.

### ContactList Screen
- Uses `bubbles/list` with two sections: **Recents** (from `RecentChats`, ordered by last activity) and **All Contacts** (from `Contacts`, alphabetical).
- Type any character to filter across both sections (built into `bubbles/list`).
- On `EventContactsReady` (fired after initial sync), both sections refresh.
- If the list is empty on first login, displays "Syncing contacts..." until the event arrives.
- `Enter` on a contact transitions to Chat.

### Chat Screen
- `bubbles/viewport` shows message history accumulated in-memory as a `[]Message` slice on the Chat model. No history is fetched from the server; history starts from the moment the conversation is opened and is discarded on `Esc`.
- Each message rendered as: `[HH:MM] You: text` or `[HH:MM] Name: text`.
- Unsupported message types (media, stickers) rendered as `[image]`, `[voice message]`, etc.
- Failed sends marked with `[!]` prefix on the message.
- `bubbles/textinput` at the bottom for composing. `Enter` sends.
- Incoming messages for the active contact are delivered via the idiomatic Bubble Tea subscription pattern: a `waitForEvent` Cmd blocks on `client.Events` and returns a `tea.Msg` when an event arrives, keeping the event loop non-blocking.
- `Esc` returns to ContactList; the `[]Message` slice is discarded.

### Command Bar (`/` overlay)
- Activated by pressing `/` from any screen.
- A single-line input appears at the bottom of the terminal.
- Recognised commands in v1:
  - `/logout` — calls `client.Logout()`, deletes `session.db`, transitions to Auth.
- Unknown commands show a brief inline error ("Unknown command").
- `Esc` dismisses the command bar without action.
- Design is extensible: future commands (`/mute`, `/clear`) require only adding a case in `commandbar.go`.

---

## Session & Storage

- Session data stored at `~/.config/wacli/session.db` (SQLite, managed by whatsmeow's `sqlstore`).
- **Startup logic:**
  - `session.db` exists → connect silently, skip Auth screen.
  - `session.db` missing → show Auth/QR screen.
- **Logout:** `client.Logout()` signals the WhatsApp server, then `session.db` is deleted. App transitions to Auth for a fresh QR scan.

---

## Connection State Indicator

- A status bar line is always visible at the bottom of the screen (above the command bar when active).
- Normal state: blank / subtle "Connected" in muted colour.
- On `EventDisconnected`: displays "Reconnecting..." with an animated colour pulse.
  - Implemented via `tea.Tick` (every 100ms) advancing a frame index in `app.go`.
  - `lipgloss` cycles through an amber/orange palette per frame — same feel as the Claude Code CLI spinner.
  - Tick is only active when connection state is `disconnected`; zero overhead otherwise.
- On reconnect (`EventConnected`): animation stops, indicator clears.

---

## Emoji Handling

- Emoji are passed through to the terminal as-is. Most modern terminal emulators render them correctly with an appropriate font (e.g. Noto Emoji, Apple Color Emoji).
- A `--no-emoji` flag forces all emoji to their `:shortcode:` text equivalents via `internal/emoji/mapper.go`. No runtime terminal introspection is attempted.
- Shortcode fallback is v1 scope; richer detection can be layered in v2 if needed.

---

## Error Handling

| Scenario | Behaviour |
|----------|-----------|
| QR code expires | whatsmeow emits new QR event; Auth screen re-renders automatically |
| Connection loss | Animated "Reconnecting..." indicator; whatsmeow retries automatically |
| Send failure | Message displayed with `[!]` prefix in chat |
| Empty contact list on first login | "Syncing contacts..." shown until `EventContactsReady` |
| Unsupported message type | Descriptive placeholder: `[image]`, `[voice message]`, `[sticker]`, etc. |
| Unknown `/command` | Inline error in command bar: "Unknown command" |

---

## Visual Design

### Palette

Terminal background is the user's default (no override). All chrome uses a small set of Lipgloss colours:

| Role | Value | Usage |
|------|-------|-------|
| White | terminal default | Message body — primary content, never overridden |
| Accent | `#00E676` (bright green) | "You" sender name, active selection, section labels, command bar `/` |
| Gray | `#888888` | Timestamps, received sender names, placeholders, media labels |
| Subtle | `#004D20` (dark green) | Subtle borders, section dividers |
| Error | `#FF5252` (red) | `[!]` failed send, unknown command error |

The palette is WhatsApp-reminiscent (white terminal + bright green accents) without forcing a background colour on the user.

### Message Rendering

Plain text lines — no bubbles. Timestamps and sender names are secondary; message body is primary:

```
11:42  You         hey, are you around?
11:43  Ana         yes! one sec
11:43  Ana         [image]
```

- Timestamp: gray — fixed width, right-padded
- Sender name: gray for others, bright green for "You" — fixed width column so message body always starts at the same x offset
- Message body: white (primary content)
- `[!]` failed sends: error red prefix
- `[image]` / `[voice message]` placeholders: gray, italicised

### ContactList

Two sections rendered with a muted green section label:

```
  RECENTS
  Ana Rodrigues          hey, are you around?
  Work Group             the doc is ready
  
  ALL CONTACTS
  Bruno Silva
  Carla Mendes
```

- Section label: muted green, small caps style (via Lipgloss `Bold`)
- Selected item: bright green text, no background fill
- Last message preview: dim grey, truncated to terminal width
- Filter input (built into `bubbles/list`): shown inline at top when typing

### Status Bar

Always visible at the bottom, one line. Normal state: empty. Disconnected state: `Reconnecting...` pulsing through `#004D20` → `#00E676` via `tea.Tick`.

### Command Bar

Appears above the status bar when active. Single line with a bright green `/` prefix and white input text.

```
/ logout█
```

## Out of Scope (v1)

- Media download and open (planned v2)
- Group chats
- Read receipts / typing indicators
- Message search
- Notifications (desktop or sound)
- Multiple simultaneous conversations
