package session

import (
	"sync"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role      Role      `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type Mode int

const (
	ModeNativeConversation Mode = iota
	ModeFallbackContext
)

type Context struct {
	mu sync.Mutex

	ID             string
	Cwd            string
	ConversationID string
	Model          string
	Mode           Mode
	Transcript     []Message
	TurnCount      int
	closed         bool
}

func NewContext(id, cwd string) *Context {
	return &Context{
		ID:         id,
		Cwd:        cwd,
		Mode:       ModeNativeConversation,
		Transcript: make([]Message, 0, 16),
	}
}

func (c *Context) AddUserMessage(content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Transcript = append(c.Transcript, Message{
		Role:      RoleUser,
		Content:   content,
		Timestamp: time.Now(),
	})
	c.TurnCount++
}

func (c *Context) AddAssistantMessage(content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Transcript = append(c.Transcript, Message{
		Role:      RoleAssistant,
		Content:   content,
		Timestamp: time.Now(),
	})
}

func (c *Context) SetConversationID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ConversationID = id
}

func (c *Context) GetConversationID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ConversationID
}

func (c *Context) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Model = model
}

func (c *Context) GetModel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Model
}

func (c *Context) SwitchToFallback() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Mode = ModeFallbackContext
}

func (c *Context) GetMode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Mode
}

func (c *Context) GetTranscript() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Message, len(c.Transcript))
	copy(out, c.Transcript)
	return out
}

func (c *Context) GetTurnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.TurnCount
}

func (c *Context) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *Context) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
