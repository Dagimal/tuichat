package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Tokens  int    `json:"tokens"`
}

type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  []Message `json:"messages"`
	Model     string    `json:"model"`
}

const lastSessionFile = "LAST"

func dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tuichat", "sessions")
}

func ensureDir() error {
	return os.MkdirAll(dir(), 0755)
}

func New(model string) *Session {
	id := time.Now().Format("20060102_150405")
	return &Session{
		ID:        id,
		Name:      "Untitled",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Model:     model,
	}
}

func (s *Session) FilePath() string {
	return filepath.Join(dir(), s.ID+".json")
}

func (s *Session) Save() error {
	ensureDir()
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(s.FilePath(), data, 0644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return SaveLastSessionID(s.ID)
}

func Load(id string) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(dir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func List() ([]*Session, error) {
	entries, err := os.ReadDir(dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []*Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if e.Name() == lastSessionFile {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(id)
		if err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (s *Session) Delete() error {
	return os.Remove(s.FilePath())
}

func (s *Session) Rename(name string) error {
	s.Name = name
	return s.Save()
}

func (s *Session) GenerateName() string {
	for _, m := range s.Messages {
		if m.Role == "user" {
			text := strings.TrimSpace(m.Content)
			text = strings.ReplaceAll(text, "\n", " ")
			if len(text) > 50 {
				text = text[:50] + "..."
			}
			return text
		}
	}
	return "Untitled"
}

func (s *Session) AddMessage(role, content string) {
	tokens := EstimateTokens(content)
	s.Messages = append(s.Messages, Message{
		Role:    role,
		Content: content,
		Tokens:  tokens,
	})
}

func SaveLastSessionID(id string) error {
	ensureDir()
	return os.WriteFile(filepath.Join(dir(), lastSessionFile), []byte(id), 0644)
}

func LoadLastSessionID() string {
	data, err := os.ReadFile(filepath.Join(dir(), lastSessionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len([]rune(text)) / 4
	if n == 0 {
		n = 1
	}
	return n
}
