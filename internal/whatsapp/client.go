package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// EventKind identifies the type of an Event.
type EventKind int

const (
	EventQRCode EventKind = iota
	EventConnected
	EventDisconnected
	EventMessage
	EventContactsReady
)

// Event carries a single notification from the WhatsApp layer to the TUI.
type Event struct {
	Kind    EventKind
	Payload any // string (QR), Message, or nil
}

// Contact represents a WhatsApp contact or chat entry.
type Contact struct {
	JID         string
	DisplayName string
	LastMessage string    // preview text, may be empty for contacts without history
	LastSeen    time.Time // zero for contacts not in recents
	Unread      bool      // true if there are unread messages
}

// Message is a single chat message for in-memory display.
type Message struct {
	ID         string // Message ID (temporary for optimistic, real from server)
	Timestamp  time.Time
	ChatJID    string // The conversation this message belongs to
	SenderJID  string
	SenderName string
	Body       string
	Failed     bool
	Pending    bool // True for optimistic messages awaiting server confirmation
	IsFromMe   bool // True if message is from the user (sent via any channel)
	// MediaType is set for non-text messages ("image", "voice message", "sticker", etc.)
	MediaType string
}

// Client wraps whatsmeow and exposes a clean channel-based interface to the TUI.
// The TUI never imports whatsmeow directly.
type Client struct {
	Events <-chan Event

	wm      *whatsmeow.Client
	db      *sql.DB // our own tables in session.db
	events  chan Event
	dbPath  string
	selfJID string

	mu             sync.Mutex
	recentActivity map[string]time.Time
	lastPreview    map[string]string    // JID → last message preview text
	chatNames      map[string]string    // JID → display name (groups + contacts from history sync)
	messageHistory map[string][]Message // JID → messages from history sync
	unreadChats    map[string]bool      // JID → true if chat has unread messages
}

// New creates a Client. Call Connect() to establish the WhatsApp session.
func New() (*Client, error) {
	dbPath, err := sessionDBPath()
	if err != nil {
		return nil, fmt.Errorf("session path: %w", err)
	}

	ch := make(chan Event, 64)
	c := &Client{
		Events:         ch,
		events:         ch,
		dbPath:         dbPath,
		recentActivity: make(map[string]time.Time),
		lastPreview:    make(map[string]string),
		chatNames:      make(map[string]string),
		messageHistory: make(map[string][]Message),
		unreadChats:    make(map[string]bool),
	}
	return c, nil
}

// initRecentChatsDB opens our own tables in session.db for persisting recent chat data.
func (c *Client) initRecentChatsDB() error {
	db, err := sql.Open("sqlite3", c.dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	c.db = db
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS wap_recent_chats (
		jid          TEXT PRIMARY KEY,
		display_name TEXT NOT NULL DEFAULT '',
		last_preview TEXT NOT NULL DEFAULT '',
		last_seen    INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// loadRecentChats restores recentActivity, lastPreview and chatNames from SQLite.
func (c *Client) loadRecentChats() {
	if c.db == nil {
		return
	}
	rows, err := c.db.Query(`SELECT jid, display_name, last_preview, last_seen FROM wap_recent_chats WHERE last_seen > 0 ORDER BY last_seen DESC`)
	if err != nil {
		return
	}
	defer rows.Close()

	c.mu.Lock()
	defer c.mu.Unlock()
	for rows.Next() {
		var jid, name, preview string
		var ts int64
		if err := rows.Scan(&jid, &name, &preview, &ts); err != nil {
			continue
		}
		// Skip self-JID entries (status updates, self-chat)
		if jid == c.selfJID {
			continue
		}
		if _, exists := c.recentActivity[jid]; !exists {
			c.recentActivity[jid] = time.Unix(ts, 0)
		}
		if name != "" {
			if _, exists := c.chatNames[jid]; !exists {
				c.chatNames[jid] = name
			}
		}
		if preview != "" {
			if _, exists := c.lastPreview[jid]; !exists {
				c.lastPreview[jid] = preview
			}
		}
	}
}

// saveRecentChat persists a single chat's data to SQLite.
func (c *Client) saveRecentChat(jid string, t time.Time, name, preview string) {
	if c.db == nil {
		return
	}
	_, _ = c.db.Exec(
		`INSERT INTO wap_recent_chats (jid, display_name, last_preview, last_seen) VALUES (?, ?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET
		   display_name = CASE WHEN excluded.display_name != '' THEN excluded.display_name ELSE wap_recent_chats.display_name END,
		   last_preview = CASE WHEN excluded.last_preview != '' THEN excluded.last_preview ELSE wap_recent_chats.last_preview END,
		   last_seen = MAX(wap_recent_chats.last_seen, excluded.last_seen)`,
		jid, name, preview, t.Unix(),
	)
}

// HasSession returns true if a WhatsApp session exists and is paired.
func (c *Client) HasSession() bool {
	if _, err := os.Stat(c.dbPath); os.IsNotExist(err) {
		return false
	}
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+c.dbPath+"?_foreign_keys=on", waLog.Noop)
	if err != nil {
		return false
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil || device == nil {
		return false
	}
	return device.ID != nil
}

// Connect opens the WhatsApp session. If a session exists, reconnects automatically.
// Otherwise, emits EventQRCode events until the user scans; on success emits EventConnected.
func (c *Client) Connect() error {
	if err := os.MkdirAll(filepath.Dir(c.dbPath), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+c.dbPath+"?_foreign_keys=on", waLog.Noop)
	// Restrict file to owner-only after sqlstore creates it.
	_ = os.Chmod(c.dbPath, 0600)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	// Initialize our own table for persisting recent chats
	if err := c.initRecentChatsDB(); err != nil {
		return fmt.Errorf("init recent chats db: %w", err)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	c.wm = whatsmeow.NewClient(device, waLog.Noop)
	c.wm.AddEventHandler(c.handleEvent)

	// If already paired, connect directly; otherwise start QR flow
	if device.ID != nil {
		// Existing session - reconnect
		if err := c.wm.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	} else {
		// New session - require QR authentication
		qrChan, _ := c.wm.GetQRChannel(ctx)
		if err := c.wm.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		go func() {
			for qr := range qrChan {
				if qr.Event == "code" {
					c.emit(Event{Kind: EventQRCode, Payload: qr.Code})
				}
			}
		}()
	}
	return nil
}

// RecentChats returns the most recently active chats from in-memory tracking,
// ordered by last message time. Limit <= 0 means no limit.
func (c *Client) RecentChats(limit int) []Contact {
	c.mu.Lock()
	type entry struct {
		jid      string
		lastSeen time.Time
	}
	entries := make([]entry, 0, len(c.recentActivity))
	for jid, t := range c.recentActivity {
		entries = append(entries, entry{jid, t})
	}
	c.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastSeen.After(entries[j].lastSeen)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	contacts := make([]Contact, 0, len(entries))
	for _, e := range entries {
		jid, err := types.ParseJID(e.jid)
		if err != nil {
			continue
		}
		contacts = append(contacts, Contact{
			JID:         e.jid,
			DisplayName: c.displayName(jid),
			LastMessage: c.lastPreview[e.jid],
			LastSeen:    e.lastSeen,
			Unread:      c.unreadChats[e.jid],
		})
	}
	return contacts
}

// Contacts returns all known contacts sorted alphabetically by display name.
func (c *Client) Contacts() []Contact {
	if c.wm == nil {
		return nil
	}
	all, err := c.wm.Store.Contacts.GetAllContacts(context.Background())
	if err != nil {
		return nil
	}

	contacts := make([]Contact, 0, len(all))
	for jid, info := range all {
		name := info.FullName
		if name == "" {
			name = info.PushName
		}
		if name == "" {
			name = jid.User
		}
		contacts = append(contacts, Contact{
			JID:         jid.String(),
			DisplayName: name,
		})
	}
	sort.Slice(contacts, func(i, j int) bool {
		return strings.ToLower(contacts[i].DisplayName) < strings.ToLower(contacts[j].DisplayName)
	})
	return contacts
}

// GetMessageHistory returns stored message history for a chat (up to 25 messages).
func (c *Client) GetMessageHistory(jid string) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	messages, ok := c.messageHistory[jid]
	if !ok {
		return nil
	}

	// Return a copy to avoid concurrent modification
	result := make([]Message, len(messages))
	copy(result, messages)
	return result
}

// MarkRead clears the unread indicator for the given chat JID.
func (c *Client) MarkRead(jid string) {
	c.mu.Lock()
	delete(c.unreadChats, jid)
	c.mu.Unlock()
}

// SyncMessages replaces the in-memory message history for a chat with the
// provided messages. Called when leaving a chat screen to persist the
// accumulated messages (including sent and real-time received ones).
func (c *Client) SyncMessages(jid string, messages []Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageHistory[jid] = make([]Message, len(messages))
	copy(c.messageHistory[jid], messages)
}

// SendText sends a plain-text message to the given JID.
func (c *Client) SendText(jid string, text string) error {
	if c.wm == nil {
		return fmt.Errorf("not connected")
	}
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("invalid JID: %w", err)
	}

	// Optimistically update recents so the preview is visible immediately
	now := time.Now()
	preview := "You: " + text
	c.mu.Lock()
	c.recentActivity[jid] = now
	c.lastPreview[jid] = preview
	c.mu.Unlock()
	c.saveRecentChat(jid, now, "", preview)

	_, err = c.wm.SendMessage(context.Background(), parsed, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return err
}

// Logout signs out from WhatsApp and deletes the local session database.
func (c *Client) Logout() error {
	if c.db != nil {
		_ = c.db.Close()
		c.db = nil
	}
	if c.wm != nil {
		_ = c.wm.Logout(context.Background())
		c.wm.Disconnect()
	}
	return os.Remove(c.dbPath)
}

// SelfJID returns the JID string for the logged-in user (empty before connect).
func (c *Client) SelfJID() string {
	return c.selfJID
}

// --- internal ---

// resolveJID converts a LID-format JID to its phone-number equivalent.
// If the JID is already a phone number or no mapping exists, returns it unchanged.
func (c *Client) resolveJID(jid types.JID) types.JID {
	if c.wm == nil {
		return jid
	}
	// Only resolve LID-type JIDs (server == "lid")
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	pn, err := c.wm.Store.LIDs.GetPNForLID(context.Background(), jid)
	if err != nil || pn.IsEmpty() {
		return jid // fallback to original if no mapping
	}
	return pn
}

func (c *Client) handleEvent(raw any) {
	switch v := raw.(type) {
	case *events.QR:
		if len(v.Codes) > 0 {
			c.emit(Event{Kind: EventQRCode, Payload: v.Codes[0]})
		}

	case *events.Connected:
		if c.wm.Store.ID != nil {
			// Only treat as "authenticated" if device is actually paired.
			// During QR setup the websocket connects first (Store.ID is nil),
			// which would otherwise prematurely transition away from the QR screen.
			resolved := c.resolveJID(c.wm.Store.ID.ToNonAD())
			c.selfJID = resolved.String()

			// Restore recent chats from our persisted table (survives restarts)
			c.loadRecentChats()

			c.emit(Event{Kind: EventConnected})

			// Don't emit ContactsReady immediately — wait for HistorySync
			// which has the full chat list. If no HistorySync arrives within
			// 3 seconds (reconnect case), emit with whatever we have from SQLite.
			go func() {
				time.Sleep(3 * time.Second)
				c.mu.Lock()
				hasData := len(c.recentActivity) > 0
				c.mu.Unlock()
				if hasData {
					c.emit(Event{Kind: EventContactsReady})
				}
			}()
		}

	case *events.Disconnected:
		c.emit(Event{Kind: EventDisconnected})

	case *events.HistorySync:
		// After fresh QR pairing the phone pushes conversation history here.
		// Populate recentActivity so RecentChats() returns useful data.
		c.populateFromHistorySync(v.Data)
		c.emit(Event{Kind: EventContactsReady})

	case *events.Contact:
		// Individual contact added/updated — refresh the list.
		c.emit(Event{Kind: EventContactsReady})

	case *events.GroupInfo:
		// Group name (subject) changed — update cache.
		if v.Name != nil {
			c.mu.Lock()
			c.chatNames[v.JID.String()] = v.Name.Name
			c.mu.Unlock()
			c.emit(Event{Kind: EventContactsReady})
		}

	case *events.Message:
		// Resolve LID JIDs to phone-number JIDs for consistent matching
		resolvedChat := c.resolveJID(v.Info.Chat.ToNonAD())
		resolvedSender := c.resolveJID(v.Info.Sender.ToNonAD())
		chatJID := resolvedChat.String()
		senderJID := resolvedSender.String()

		// Skip messages where the chat is our own JID (status updates,
		// self-chat echoes) — these create ghost entries in recents.
		if chatJID == c.selfJID {
			return
		}

		preview := extractPreview(v.Message)
		if preview != "" && v.Info.IsFromMe {
			preview = "You: " + preview
		}
		c.mu.Lock()
		c.recentActivity[chatJID] = v.Info.Timestamp
		if preview != "" {
			c.lastPreview[chatJID] = preview
		}
		if !v.Info.IsFromMe {
			c.unreadChats[chatJID] = true
		}
		c.mu.Unlock()

		// Persist to SQLite — use empty name so we don't overwrite a good
		// name from HistorySync with a fallback numeric JID.
		c.saveRecentChat(chatJID, v.Info.Timestamp, "", preview)

		senderName := c.displayName(resolvedSender)
		msg := Message{
			ID:         v.Info.ID,
			Timestamp:  v.Info.Timestamp,
			ChatJID:    chatJID,
			SenderJID:  senderJID,
			SenderName: senderName,
			IsFromMe:   v.Info.IsFromMe,
			Pending:    false,
		}
		switch {
		case v.Message.GetConversation() != "":
			msg.Body = v.Message.GetConversation()
		case v.Message.GetExtendedTextMessage() != nil:
			msg.Body = v.Message.GetExtendedTextMessage().GetText()
		case v.Message.GetImageMessage() != nil:
			msg.MediaType = "image"
		case v.Message.GetVideoMessage() != nil:
			msg.MediaType = "video"
		case v.Message.GetAudioMessage() != nil:
			msg.MediaType = "voice message"
		case v.Message.GetStickerMessage() != nil:
			msg.MediaType = "sticker"
		case v.Message.GetDocumentMessage() != nil:
			msg.MediaType = "document"
		default:
			msg.MediaType = "unsupported message"
		}

		// Store in messageHistory so the message is available when opening the chat
		c.mu.Lock()
		c.messageHistory[chatJID] = append(c.messageHistory[chatJID], msg)
		c.mu.Unlock()

		c.emit(Event{Kind: EventMessage, Payload: msg})
	}
}

func (c *Client) populateFromHistorySync(data *waHistorySync.HistorySync) {
	if data == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conv := range data.GetConversations() {
		rawJID := conv.GetID()
		if rawJID == "" {
			continue
		}
		// Normalize JID to match the format used by EventMessage
		parsed, err := types.ParseJID(rawJID)
		if err != nil {
			continue
		}
		resolved := c.resolveJID(parsed.ToNonAD())
		jid := resolved.String()

		// Skip self-JID entries (status updates, self-chat)
		if jid == c.selfJID {
			continue
		}

		ts := conv.GetConversationTimestamp()
		if ts == 0 {
			ts = conv.GetLastMsgTimestamp()
		}
		if ts == 0 {
			continue
		}
		t := time.Unix(int64(ts), 0)
		if existing, ok := c.recentActivity[jid]; !ok || t.After(existing) {
			c.recentActivity[jid] = t
		}
		// Cache the conversation name (group subject or contact push name).
		name := conv.GetName()
		if name != "" {
			c.chatNames[jid] = name
		}

		// Persist to SQLite (preview will be filled below if available)
		c.saveRecentChat(jid, t, name, "")

		// Store message history (up to 25 messages per chat)
		var messages []Message
		for i, hsMsg := range conv.GetMessages() {
			if i >= 25 {
				break // Limit to 25 messages
			}
			msg := messageFromHistorySync(hsMsg, jid, c.selfJID)
			if msg.Body != "" || msg.MediaType != "" {
				messages = append(messages, msg)
			}

			// Also extract preview from first message
			if i == 0 {
				if _, hasPreview := c.lastPreview[jid]; !hasPreview {
					if info := hsMsg.GetMessage(); info != nil {
						preview := extractPreview(info.GetMessage())
						if preview != "" {
							if info.GetKey().GetFromMe() {
								preview = "You: " + preview
							}
							c.lastPreview[jid] = preview
							c.saveRecentChat(jid, t, "", preview)
						}
					}
				}
			}
		}

		// Store messages in reverse order (oldest first for display)
		if len(messages) > 0 {
			for i := len(messages) - 1; i >= 0; i-- {
				c.messageHistory[jid] = append(c.messageHistory[jid], messages[i])
			}
		}
	}
}

// extractPreview pulls a short text snippet from a WhatsApp message.
func extractPreview(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if t := msg.GetConversation(); t != "" {
		return t
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if msg.GetImageMessage() != nil {
		return "[image]"
	}
	if msg.GetVideoMessage() != nil {
		return "[video]"
	}
	if msg.GetAudioMessage() != nil {
		return "[voice message]"
	}
	if msg.GetStickerMessage() != nil {
		return "[sticker]"
	}
	if msg.GetDocumentMessage() != nil {
		return "[document]"
	}
	return ""
}

func (c *Client) emit(e Event) {
	select {
	case c.events <- e:
	default:
		// drop if channel is full rather than blocking the whatsmeow event loop
	}
}

func (c *Client) displayName(jid types.JID) string {
	// For groups, the contact store has nothing — use the cached name from history sync.
	if jid.Server == types.GroupServer {
		c.mu.Lock()
		name := c.chatNames[jid.String()]
		c.mu.Unlock()
		if name != "" {
			return name
		}
		return jid.User // fallback to numerical ID until history sync arrives
	}

	if c.wm == nil {
		return jid.User
	}
	// Check chatNames first (populated from history sync, may have richer push names).
	// Skip entries that look like raw JIDs (contain "@") — history sync sometimes
	// returns the JID itself as the name when no display name is available.
	c.mu.Lock()
	cached := c.chatNames[jid.String()]
	c.mu.Unlock()
	if cached != "" && !strings.Contains(cached, "@") {
		return cached
	}
	info, err := c.wm.Store.Contacts.GetContact(context.Background(), jid)
	if err == nil {
		if info.FullName != "" {
			return info.FullName
		}
		if info.PushName != "" {
			return info.PushName
		}
	}
	return jid.User
}

func messageFromHistorySync(hsMsg *waHistorySync.HistorySyncMsg, chatJID, selfJID string) Message {
	info := hsMsg.GetMessage()
	if info == nil {
		return Message{}
	}

	key := info.GetKey()
	waMsg := info.GetMessage()

	senderJID := key.GetParticipant()
	if senderJID == "" {
		senderJID = key.GetRemoteJID()
	}

	// Extract sender name from push name in message
	senderName := info.GetPushName()
	if senderName == "" && waMsg != nil {
		// Try to get name from message context
		if key.GetFromMe() {
			senderName = "You"
		}
	}

	msg := Message{
		Timestamp:  time.Unix(int64(info.GetMessageTimestamp()), 0),
		ChatJID:    chatJID,
		SenderJID:  senderJID,
		SenderName: senderName,
		IsFromMe:   key.GetFromMe(),
	}

	switch {
	case waMsg.GetConversation() != "":
		msg.Body = waMsg.GetConversation()
	case waMsg.GetExtendedTextMessage() != nil:
		msg.Body = waMsg.GetExtendedTextMessage().GetText()
	case waMsg.GetImageMessage() != nil:
		msg.MediaType = "image"
	case waMsg.GetVideoMessage() != nil:
		msg.MediaType = "video"
	case waMsg.GetAudioMessage() != nil:
		msg.MediaType = "voice message"
	case waMsg.GetStickerMessage() != nil:
		msg.MediaType = "sticker"
	case waMsg.GetDocumentMessage() != nil:
		msg.MediaType = "document"
	default:
		msg.MediaType = "unsupported message"
	}

	return msg
}

func sessionDBPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "wap", "session.db"), nil
}
