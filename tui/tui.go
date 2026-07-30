package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/atotto/clipboard"
	"tuichat/config"
	ctxmgr "tuichat/context"
	"tuichat/llm"
	"tuichat/mcp"
	"tuichat/session"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

type state int

const (
	chatState state = iota
	modelListState
	sessionListState
)

type chatMessage struct {
	role    string
	content string
}

type streamProgressMsg string
type streamDoneMsg struct{}
type editorResultMsg struct{ content string }
type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

type readResultMsg struct{ content string }
type mcpProgressMsg string
type mcpDoneMsg struct {
	content  string
	inputTok int
	outTok   int
	ctxTok   int
}

type modelItem struct {
	provider *config.Provider
	model    config.Model
}

func (i modelItem) Title() string       { return i.model.Name }
func (i modelItem) Description() string { return fmt.Sprintf("%s • %s", i.provider.Name, i.model.ID) }
func (i modelItem) FilterValue() string {
	return i.model.Name + " " + i.model.ID + " " + i.provider.Name
}

type sessionItem struct {
	sess *session.Session
}

func (i sessionItem) Title() string       { return i.sess.Name }
func (i sessionItem) Description() string { return fmt.Sprintf("%s • %d msgs", i.sess.CreatedAt.Format("2006-01-02 15:04"), len(i.sess.Messages)) }
func (i sessionItem) FilterValue() string { return i.sess.Name + " " + i.sess.CreatedAt.Format("2006-01-02") }

type model struct {
	state           state
	cfg             *config.Config
	messages        []chatMessage
	input           textinput.Model
	vp              viewport.Model
	mdList          list.Model
	activeModel     string
	activeProvider  *config.Provider
	loading         bool
	currentResp     string
	sub             chan tea.Msg
	ready           bool
	width           int
	height          int
	spinner         spinner.Model
	cancel          context.CancelFunc
	history         []string
	historyIdx      int
	savedInput      string
	currentSession  *session.Session
	sessions        []*session.Session
	renameTarget    *session.Session
	rendered        []string
	filterText      string
	suggestions     []string
	suggestionIdx   int
	showSuggestions bool
	inputTokens     int
	outputTokens    int
	ctxTokens       int
	ctxMgr          *ctxmgr.Manager
	autoScroll      bool
	mcpToolCache    map[string][]llm.ToolDefinition
}

var cavemanPrompts = map[string]string{
	"lite":  "Lite compact mode. No filler (just/really/basically/actually/simply). No pleasantries (sure/certainly/of course). No hedging. Keep full sentences and articles. Technical terms exact. Never announce or name the style. Preserve my language.",
	"full":  "Full compact mode. Drop articles (a/an/the). Drop filler (just/really/basically/actually/simply). Drop pleasantries (sure/certainly/of course). Use short synonyms. Fragments OK. No decorative emoji/tables. No explanations unless asked. Technical terms exact. Standard acronyms OK (DB/API/HTTP); no invented abbreviations. Pattern: [thing] [action] [reason]. [next step]. Never announce or name the style. Preserve my language.",
	"ultra": "Ultra compact mode. Drop articles/conjunctions where possible. Arrows for causality (X → Y). One word when enough. Abbreviate prose words only (DB/auth/config/req/res) — NEVER abbreviate code symbols, function names, API names, error strings. No greetings. Max 2-3 sentences. Technical terms exact. Never announce or name the style. Preserve my language.",
}

var (
	userColor       = lipgloss.Color("#7D56F4")
	assistantColor  = lipgloss.Color("#00D68F")
	systemColor     = lipgloss.Color("#6B6B6B")
	statusBg        = lipgloss.Color("#2A2A2A")
	suggestBg       = lipgloss.Color("#1E1E2E")
	suggestSelBg    = lipgloss.Color("#313244")
	suggestText     = lipgloss.Color("#CDD6F4")
	suggestDesc     = lipgloss.Color("#6C7086")
)

var commands = []struct {
	cmd  string
	desc string
}{
	{"/model", "Switch model"},
	{"/sessions", "Manage sessions"},
	{"/reset", "Clear history"},
	{"/compact", "Summarize old messages to save tokens"},
	{"/caveman", "Toggle compact mode: off/lite/full/ultra"},
	{"/cp", "Copy last code block: /cp [N|all]"},
	{"/read", "Read file: /read <path>"},
	{"/mcp", "Run MCP tool: /mcp <server> <instruction>"},
	{"/edit", "Open editor (ctrl+e)"},
	{"/new", "New session"},
	{"/rename", "Rename session"},
	{"/exit", "Quit"},
}

func New(cfg *config.Config) *model {
	ti := textinput.New()
	ti.Placeholder = "Type a message... (/read, /model, /new, /edit, /sessions, /reset, /exit)"
	ti.Focus()
	ti.CharLimit = 0
	ti.SetWidth(80)

	s := spinner.New(spinner.WithSpinner(spinner.Dot))

	var activeProvider *config.Provider
	for i := range cfg.Providers {
		for _, m := range cfg.Providers[i].Models {
			if m.ID == cfg.DefaultModel {
				activeProvider = &cfg.Providers[i]
				break
			}
		}
		if activeProvider != nil {
			break
		}
	}
	if activeProvider == nil && len(cfg.Providers) > 0 {
		activeProvider = &cfg.Providers[0]
	}

	activeModel := cfg.DefaultModel
	if activeProvider != nil && activeModel == "" && len(activeProvider.Models) > 0 {
		activeModel = activeProvider.Models[0].ID
	}

	delegate := list.NewDefaultDelegate()
	items := buildModelItems(cfg)
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select Model"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()

	// Try to resume last session
	budget := cfg.TokenBudget
	if budget <= 0 {
		budget = 40000
	}
	m := &model{
		cfg:            cfg,
		input:          ti,
		mdList:         l,
		activeModel:    activeModel,
		activeProvider: activeProvider,
		sub:            make(chan tea.Msg, 128),
		spinner:        s,
		ctxMgr:         ctxmgr.New(budget, 20),
		autoScroll:     true,
		mcpToolCache:   make(map[string][]llm.ToolDefinition),
	}
	m.currentSession = session.New(activeModel)
	if cfg.CavemanMode != "" {
		m.currentSession.CavemanMode = cfg.CavemanMode
	}
	return m
}

func toChatMessages(s *session.Session) []chatMessage {
	var msgs []chatMessage
	for _, m := range s.Messages {
		msgs = append(msgs, chatMessage{role: m.Role, content: m.Content})
	}
	return msgs
}

func countTokens(msgs []session.Message, role string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n += m.Tokens
		}
	}
	return n
}

func buildModelItems(cfg *config.Config) []list.Item {
	var items []list.Item
	for i := range cfg.Providers {
		for _, m := range cfg.Providers[i].Models {
			items = append(items, modelItem{provider: &cfg.Providers[i], model: m})
		}
	}
	return items
}

func buildSessionItems(sessions []*session.Session) []list.Item {
	var items []list.Item
	for _, s := range sessions {
		items = append(items, sessionItem{sess: s})
	}
	return items
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.vp = viewport.New(
			viewport.WithWidth(msg.Width),
			viewport.WithHeight(msg.Height-4-m.suggestLines()),
		)
		m.vp.YPosition = 0
		m.vp.MouseWheelEnabled = true
		m.input.SetWidth(msg.Width - 4)
		m.mdList.SetWidth(msg.Width)
		m.mdList.SetHeight(msg.Height - 4)
		m.ready = true
		m.updateViewportContent()
		return m, nil

	case tea.KeyPressMsg:
		switch m.state {
		case modelListState:
			return m.handleModelListKey(msg)
		case sessionListState:
			return m.handleSessionListKey(msg)
		}
		return m.handleChatKey(msg)

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.autoScroll = m.vp.AtBottom()
		return m, cmd

	case tea.PasteMsg:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case streamProgressMsg:
		m.currentResp += string(msg)
		m.updateViewportContent()
		if m.autoScroll {
			m.vp.GotoBottom()
		}
		return m, m.waitForStream()

	case streamDoneMsg:
		m.messages = append(m.messages, chatMessage{role: "assistant", content: m.currentResp})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.outputTokens += session.EstimateTokens(m.currentResp)
		m.currentSession.AddMessage("assistant", m.currentResp)
		m.currentSession.Model = m.activeModel
		if m.currentSession.Name == "Untitled" {
			m.currentSession.Name = m.currentSession.GenerateName()
		}
		m.currentSession.Save()
		m.currentResp = ""
		m.loading = false
		m.updateViewportContent()
		if m.autoScroll {
			m.vp.GotoBottom()
		}
		return m, nil

	case readResultMsg:
		if !m.loading {
			return m, nil
		}
		m.loading = false
		m.messages = append(m.messages, chatMessage{role: "assistant", content: msg.content})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.outputTokens += session.EstimateTokens(msg.content)
		m.currentSession.AddMessage("assistant", msg.content)
		m.currentSession.Save()
		m.currentResp = ""
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, nil

	case mcpProgressMsg:
		m.currentResp = m.currentResp + string(msg) + "\n"
		m.updateViewportContent()
		if m.autoScroll {
			m.vp.GotoBottom()
		}
		return m, m.waitForStream()

	case mcpDoneMsg:
		m.loading = false
		m.inputTokens += msg.inputTok
		m.outputTokens += msg.outTok
		m.ctxTokens = msg.ctxTok
		if m.currentResp != "" {
			m.messages = append(m.messages, chatMessage{role: "assistant", content: strings.TrimSpace(m.currentResp)})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		}
		m.currentResp = ""
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, nil

	case editorResultMsg:
		content := strings.TrimSpace(msg.content)
		if content != "" {
			m.input.SetValue(content)
			m.input.CursorEnd()
		}
		return m, nil

	case errMsg:
		m.messages = append(m.messages, chatMessage{
			role:    "system",
			content: fmt.Sprintf("Error: %v", msg.err),
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.loading = false
		m.currentResp = ""
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.loading {
			m.updateViewportContent()
		}
		return m, cmd
	}

	return m, nil
}

func (m *model) suggestLines() int {
	if !m.showSuggestions || len(m.suggestions) == 0 {
		return 0
	}
	n := len(m.suggestions)
	if n > 4 {
		n = 4
	}
	return n
}

func (m *model) handleChatKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Keystroke() {
	case "ctrl+e":
		return m, m.openEditor()

	case "ctrl+c":
		if len(m.messages) > 0 {
			m.currentSession.Save()
		}
		return m, tea.Quit

	case "enter":
		if m.showSuggestions && len(m.suggestions) > 0 {
			sel := m.suggestions[m.suggestionIdx]
			m.input.SetValue(sel + " ")
			m.input.CursorEnd()
			m.showSuggestions = false
			m.suggestions = nil
			m.vp.SetHeight(m.height - 4)
			return m, nil
		}
		if m.loading {
			return m, nil
		}
		if m.activeProvider == nil {
			m.messages = append(m.messages, chatMessage{
				role:    "system",
				content: "No providers configured. Edit config.yaml.",
			})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}

		input := m.input.Value()
		m.input.SetValue("")
		input = strings.TrimSpace(input)

		if strings.HasPrefix(input, "/") {
			m.showSuggestions = false
			m.suggestions = nil
			return m.handleCommand(input)
		}
		if input == "" {
			return m, nil
		}

		m.history = append(m.history, input)
		m.historyIdx = -1
		m.savedInput = ""

		m.messages = append(m.messages, chatMessage{role: "user", content: input})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.inputTokens += session.EstimateTokens(input)
		m.currentSession.AddMessage("user", input)
		m.currentSession.Save()
		m.loading = true
		m.currentResp = ""
		m.autoScroll = true
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, m.startStream(input)

	case "tab":
		if m.showSuggestions && len(m.suggestions) > 0 {
			sel := m.suggestions[m.suggestionIdx]
			m.input.SetValue(sel + " ")
			m.input.CursorEnd()
			m.showSuggestions = false
			m.suggestions = nil
			if m.height > 0 {
				m.vp.SetHeight(m.height - 4)
			}
			return m, nil
		}
		return m, nil

	case "esc":
		if m.showSuggestions {
			m.showSuggestions = false
			m.suggestions = nil
			if m.height > 0 {
				m.vp.SetHeight(m.height - 4)
			}
			return m, nil
		}
		if m.loading && m.cancel != nil {
			m.cancel()
			m.loading = false
			m.currentResp = ""
			m.messages = append(m.messages, chatMessage{role: "system", content: "Cancelled."})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			m.vp.GotoBottom()
		}
		return m, nil

		case "up":
		if m.showSuggestions {
			if m.suggestionIdx > 0 {
				m.suggestionIdx--
			}
			return m, nil
		}
		if m.historyIdx != -1 || len(m.history) > 0 {
			if m.historyIdx == -1 {
				m.historyIdx = len(m.history) - 1
				m.savedInput = m.input.Value()
			} else if m.historyIdx > 0 {
				m.historyIdx--
			} else {
				var vpCmd tea.Cmd
				m.vp, vpCmd = m.vp.Update(msg)
				m.autoScroll = m.vp.AtBottom()
				return m, vpCmd
			}
			m.input.SetValue(m.history[m.historyIdx])
			m.input.CursorEnd()
			return m, nil
		}
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		m.autoScroll = m.vp.AtBottom()
		return m, vpCmd

	case "down":
		if m.showSuggestions {
			if m.suggestionIdx < len(m.suggestions)-1 {
				m.suggestionIdx++
			}
			return m, nil
		}
		if m.historyIdx != -1 {
			if m.historyIdx < len(m.history)-1 {
				m.historyIdx++
				m.input.SetValue(m.history[m.historyIdx])
			} else {
				m.historyIdx = -1
				m.input.SetValue(m.savedInput)
			}
			m.input.CursorEnd()
			return m, nil
		}
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		m.autoScroll = m.vp.AtBottom()
		return m, vpCmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateSuggestions()
	return m, cmd
}

func (m *model) updateSuggestions() {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") {
		m.showSuggestions = false
		m.suggestions = nil
		return
	}
	var filtered []string
	for _, c := range commands {
		if strings.HasPrefix(c.cmd, val) {
			filtered = append(filtered, c.cmd)
		}
	}
	if len(filtered) == 0 {
		m.showSuggestions = false
		m.suggestions = nil
		if m.height > 0 {
			m.vp.SetHeight(m.height - 4)
		}
		return
	}
	m.suggestions = filtered
	if m.suggestionIdx >= len(m.suggestions) {
		m.suggestionIdx = len(m.suggestions) - 1
	}
	m.showSuggestions = true
	if m.height > 0 {
		m.vp.SetHeight(m.height - 4 - m.suggestLines())
	}
}

func (m *model) handleModelListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Keystroke() {
	case "enter":
		selected, ok := m.mdList.SelectedItem().(modelItem)
		if ok {
			m.activeProvider = selected.provider
			m.activeModel = selected.model.ID
		}
		m.state = chatState
		m.input.Focus()
		return m, nil

	case "esc":
		m.state = chatState
		m.input.Focus()
		return m, nil
	}

	var cmd tea.Cmd
	m.mdList, cmd = m.mdList.Update(msg)
	return m, cmd
}

func (m *model) handleSessionListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.renameTarget != nil {
		switch msg.Keystroke() {
		case "enter":
			name := strings.TrimSpace(m.input.Value())
			if name != "" {
				m.renameTarget.Rename(name)
			}
			m.renameTarget = nil
			m.input.SetValue("")
			m.input.Blur()
			m.refreshSessionList()
			return m, nil
		case "esc":
			m.renameTarget = nil
			m.input.SetValue("")
			m.input.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch msg.Keystroke() {
	case "enter":
		selected, ok := m.mdList.SelectedItem().(sessionItem)
		if ok {
			m.loadSession(selected.sess)
		}
		m.state = chatState
		m.input.Focus()
		return m, nil

	case "esc":
		if m.filterText != "" {
			m.filterText = ""
			m.mdList.SetItems(buildSessionItems(m.sessions))
			m.mdList.GoToStart()
			return m, nil
		}
		m.state = chatState
		m.input.Focus()
		return m, nil

	case "ctrl+r":
		selected, ok := m.mdList.SelectedItem().(sessionItem)
		if ok {
			m.renameTarget = selected.sess
			m.input.SetValue(selected.sess.Name)
			m.input.CursorEnd()
			m.input.Focus()
			return m, nil
		}
		return m, nil

	case "ctrl+d":
		selected, ok := m.mdList.SelectedItem().(sessionItem)
		if ok {
			selected.sess.Delete()
			m.refreshSessionList()
		}
		return m, nil
	}

	// Printable character → filter sessions manually
	k := msg.Keystroke()
	if len(k) == 1 && k[0] >= ' ' && k[0] <= '~' {
		m.filterText += k
		m.applySessionFilter()
		return m, nil
	}
	if k == "backspace" {
		if m.filterText != "" {
			m.filterText = m.filterText[:len(m.filterText)-1]
			if m.filterText == "" {
				m.mdList.SetItems(buildSessionItems(m.sessions))
			} else {
				m.applySessionFilter()
			}
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.mdList, cmd = m.mdList.Update(msg)
	return m, cmd
}

func (m *model) applySessionFilter() {
	var filtered []list.Item
	for _, s := range m.sessions {
		if strings.Contains(strings.ToLower(s.Name), strings.ToLower(m.filterText)) {
			filtered = append(filtered, sessionItem{sess: s})
		}
	}
	m.mdList.SetItems(filtered)
	m.mdList.GoToStart()
}

func (m *model) loadSession(s *session.Session) {
	m.currentSession = s
	m.messages = toChatMessages(s)
	m.invalidateCache()
	m.inputTokens = countTokens(s.Messages, "user")
	m.outputTokens = countTokens(s.Messages, "assistant")
	m.ctxTokens = 0
	m.activeModel = s.Model
	m.loading = false
	m.currentResp = ""
	m.autoScroll = true
	m.updateViewportContent()
	m.vp.GotoBottom()
}

func (m *model) refreshSessionList() {
	m.sessions, _ = session.List()
	m.mdList.SetItems(buildSessionItems(m.sessions))
	m.mdList.SetShowFilter(true)
}

func (m *model) handleCommand(input string) (tea.Model, tea.Cmd) {
	switch {
	case input == "/exit":
		if len(m.messages) > 0 {
			m.currentSession.Save()
		}
		return m, tea.Quit

	case input == "/reset":
		if m.cancel != nil {
			m.cancel()
		}
		m.messages = nil
		m.rendered = nil
		m.currentResp = ""
		m.loading = false
		m.inputTokens = 0
		m.outputTokens = 0
		m.ctxTokens = 0
		m.currentSession = session.New(m.activeModel)
		m.updateViewportContent()
		return m, nil

	case input == "/model" || input == "/switch":
		m.state = modelListState
		m.mdList.SetItems(buildModelItems(m.cfg))
		m.mdList.ResetSelected()
		m.input.Blur()
		return m, nil

	case input == "/sessions":
		sessions, err := session.List()
		if err != nil || len(sessions) == 0 {
			m.messages = append(m.messages, chatMessage{
				role:    "system",
				content: "No saved sessions.",
			})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}
		m.sessions = sessions
		m.filterText = ""
		m.state = sessionListState
		cmd := m.mdList.SetItems(buildSessionItems(sessions))
		m.mdList.ResetSelected()
		m.mdList.GoToStart()
		m.mdList.Title = "Sessions (ctrl+r=rename  ctrl+d=delete)"
		m.input.Blur()
		return m, cmd

	case input == "/cp", strings.HasPrefix(input, "/cp "):
		arg := ""
		if strings.HasPrefix(input, "/cp ") {
			arg = strings.TrimPrefix(input, "/cp ")
		}
		return m.handleCopy(arg)

	case input == "/caveman", strings.HasPrefix(input, "/caveman "):
		s := m.currentSession
		arg := strings.TrimSpace(strings.TrimPrefix(input, "/caveman"))
		levels := []string{"lite", "full", "ultra"}
		switch arg {
		case "lite", "full", "ultra":
			s.CavemanMode = arg
		case "off":
			s.CavemanMode = ""
		case "":
			if s.CavemanMode == "" {
				s.CavemanMode = "lite"
			} else {
				for i, l := range levels {
					if l == s.CavemanMode {
						if i+1 < len(levels) {
							s.CavemanMode = levels[i+1]
						} else {
							s.CavemanMode = ""
						}
						break
					}
				}
			}
		default:
			m.messages = append(m.messages, chatMessage{
				role: "system", content: "Usage: /caveman [lite|full|ultra|off]",
			})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}
		s.Save()
		status := s.CavemanMode
		if status == "" {
			status = "off"
		}
		m.messages = append(m.messages, chatMessage{
			role: "system", content: fmt.Sprintf("Caveman mode: %s", status),
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, nil

	case strings.HasPrefix(input, "/new "):
		name := strings.TrimSpace(strings.TrimPrefix(input, "/new "))
		if name == "" {
			m.messages = append(m.messages, chatMessage{
				role: "system", content: "Usage: /new <session name>",
			})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}
		if len(m.messages) > 0 {
			m.currentSession.Save()
		}
		m.messages = nil
		m.rendered = nil
		m.currentResp = ""
		m.loading = false
		m.inputTokens = 0
		m.outputTokens = 0
		m.ctxTokens = 0
		m.currentSession = session.New(m.activeModel)
		m.currentSession.Name = name
		m.updateViewportContent()
		return m, nil

	case input == "/edit":
		return m, m.openEditor()

	case strings.HasPrefix(input, "/read "):
		return m.handleRead(strings.TrimPrefix(input, "/read "))

	case strings.HasPrefix(input, "/mcp "):
		return m.handleMCP(strings.TrimPrefix(input, "/mcp "))

	case strings.HasPrefix(input, "/rename "):
		name := strings.TrimPrefix(input, "/rename ")
		if m.currentSession != nil && strings.TrimSpace(name) != "" {
			m.currentSession.Rename(strings.TrimSpace(name))
		}
		return m, nil

	default:
		m.messages = append(m.messages, chatMessage{
			role:    "system",
			content: fmt.Sprintf("Unknown: %s\nAvailable: /model, /new, /sessions, /reset, /exit", input),
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}
}

func (m *model) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	f, err := os.CreateTemp("", "tuichat-*.md")
	if err != nil {
		return func() tea.Msg { return errMsg{err} }
	}
	f.WriteString(m.input.Value())
	tmpPath := f.Name()
	f.Close()

	c := exec.Command(editor, tmpPath)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return errMsg{err}
		}
		data, rerr := os.ReadFile(tmpPath)
		if rerr != nil {
			return errMsg{rerr}
		}
		return editorResultMsg{content: string(data)}
	})
}

func (m *model) handleRead(arg string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(arg)
	if len(parts) == 0 {
		m.messages = append(m.messages, chatMessage{
			role: "system", content: "Usage: /read <path> [instruction]",
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	path := parts[0]
	instruction := strings.Join(parts[1:], " ")

	absPath, err := resolvePath(path)
	if err != nil {
		m.messages = append(m.messages, chatMessage{
			role: "system", content: fmt.Sprintf("error: %v", err),
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		m.messages = append(m.messages, chatMessage{
			role: "system", content: fmt.Sprintf("error: %v", err),
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	fullInput := fmt.Sprintf("/read %s", arg)
	m.messages = append(m.messages, chatMessage{role: "user", content: fullInput})
	m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
	m.history = append(m.history, fullInput)
	m.historyIdx = -1
	m.savedInput = ""
	m.inputTokens += session.EstimateTokens(fullInput)

	if info.IsDir() {
		entries, err := os.ReadDir(absPath)
		content := ""
		if err != nil {
			content = fmt.Sprintf("error reading directory: %v", err)
		} else {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			content = fmt.Sprintf("📁 %s (%d items)\n\n%s", absPath, len(names), strings.Join(names, "\n"))
		}
		m.messages = append(m.messages, chatMessage{role: "assistant", content: content})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		m.messages = append(m.messages, chatMessage{
			role: "system", content: fmt.Sprintf("error reading file: %v", err),
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	if instruction == "" {
		content := fmt.Sprintf("📄 %s\n\n%s", absPath, string(data))
		m.messages = append(m.messages, chatMessage{role: "assistant", content: content})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		m.vp.GotoBottom()
		return m, nil
	}

	if m.cancel != nil {
		m.cancel()
	}

	if m.activeProvider == nil {
		m.messages = append(m.messages, chatMessage{
			role: "system", content: "No provider configured.",
		})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	msgs := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant. The user has provided a file and an instruction. Follow the instruction precisely and return the result."},
		{Role: "user", Content: fmt.Sprintf("File: %s\n\n%s\n\nInstruction: %s", absPath, string(data), instruction)},
	}

	m.ctxTokens = 0
	for _, msg := range msgs {
		m.ctxTokens += session.EstimateTokens(msg.Content)
	}

	baseURL := m.activeProvider.BaseURL
	apiKey := m.activeProvider.APIKey
	modelID := m.activeModel

	m.loading = true
	m.currentResp = ""

	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	sub := make(chan tea.Msg, 128)
	m.sub = sub

	go func() {
		defer cancel()
		response, err := llm.Chat(baseURL, apiKey, modelID, msgs)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			sub <- errMsg{err}
			return
		}
		sub <- readResultMsg{content: response}
	}()

	return m, m.waitForStream()
}

func (m *model) handleMCP(arg string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(arg)
	if len(parts) < 2 {
		m.messages = append(m.messages, chatMessage{role: "system", content: "Usage: /mcp <server> <instruction>"})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}
	serverName := parts[0]
	instruction := strings.Join(parts[1:], " ")

	if m.cancel != nil {
		m.cancel()
	}

	if m.activeProvider == nil {
		m.messages = append(m.messages, chatMessage{role: "system", content: "No provider configured."})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	if m.cfg.MCP == nil || m.cfg.MCP.Servers == nil {
		m.messages = append(m.messages, chatMessage{role: "system", content: "No MCP servers configured in config.yaml."})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	entry, ok := m.cfg.MCP.Servers[serverName]
	if !ok {
		m.messages = append(m.messages, chatMessage{role: "system", content: fmt.Sprintf("Unknown MCP server: %s", serverName)})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	fullInput := fmt.Sprintf("/mcp %s", arg)
	m.messages = append(m.messages, chatMessage{role: "user", content: fullInput})
	m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
	m.history = append(m.history, fullInput)
	m.historyIdx = -1
	m.savedInput = ""
	m.loading = true
	m.currentResp = ""

	sub := make(chan tea.Msg, 128)
	m.sub = sub
	baseURL := m.activeProvider.BaseURL
	apiKey := m.activeProvider.APIKey
	modelID := m.activeModel

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	go func() {
		defer cancel()

		send := func(msg string) {
			select {
			case sub <- mcpProgressMsg(msg):
			default:
			}
		}
		fail := func(msg string) {
			select {
			case sub <- mcpDoneMsg{content: msg}:
			default:
			}
		}

		if ctx.Err() != nil {
			return
		}

		send(fmt.Sprintf("starting MCP server: %s...", serverName))
		client, err := mcp.NewClient(entry.Command, entry.Args)
		if err != nil {
			fail(fmt.Sprintf("MCP error: %v", err))
			return
		}
		defer client.Close()

		var toolDefs []llm.ToolDefinition
		if cached, ok := m.mcpToolCache[serverName]; ok {
			toolDefs = cached
			send(fmt.Sprintf("tools loaded (%d, cached)", len(toolDefs)))
		} else {
			tools, err := client.ListTools()
			if err != nil {
				stderr := client.Stderr()
				if stderr != "" {
					stderr = "\nstderr: " + stderr
				}
				fail(fmt.Sprintf("MCP list tools error: %v%s", err, stderr))
				return
			}
			toolDefs = mcp.ToolsToLLM(tools)
			m.mcpToolCache[serverName] = toolDefs
			send(fmt.Sprintf("tools loaded: %d", len(tools)))
		}

		if ctx.Err() != nil {
			return
		}

		msgs := []llm.Message{
			{Role: "system", Content: fmt.Sprintf("You have access to MCP tools on %s. Use them to fulfill the user's request.", serverName)},
			{Role: "user", Content: instruction},
		}

		inputTok := 0
		for _, msg := range msgs {
			inputTok += session.EstimateTokens(msg.Content)
		}
		toolJSON, _ := json.Marshal(toolDefs)
		inputTok += len(string(toolJSON)) / 4
		ctxTok := inputTok
		outTok := 0

		for iter := 0; iter < 8; iter++ {
			resp, err := llm.ChatWithTools(baseURL, apiKey, modelID, msgs, toolDefs)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fail(fmt.Sprintf("LLM error: %v", err))
				return
			}

			outTok += session.EstimateTokens(resp.Content)
			for _, tc := range resp.ToolCalls {
				outTok += session.EstimateTokens(tc.Function.Name + tc.Function.Arguments)
			}

			if len(resp.ToolCalls) == 0 {
				select {
				case sub <- mcpDoneMsg{content: resp.Content, inputTok: inputTok, outTok: outTok, ctxTok: ctxTok}:
				default:
				}
				return
			}

			msgs = append(msgs, resp)
			for _, tc := range resp.ToolCalls {
				var args map[string]interface{}
				json.Unmarshal([]byte(tc.Function.Arguments), &args)
				send(fmt.Sprintf("calling %s...", tc.Function.Name))
				result, err := client.CallTool(tc.Function.Name, args)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					result = fmt.Sprintf("tool error: %v", err)
				}
				msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: result})
			}
		}

		fail("MCP: max iterations reached")
	}()

	return m, m.waitForStream()
}

func (m *model) handleCopy(arg string) (tea.Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)

	if arg == "all" {
		var lastContent string
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].role == "assistant" || m.messages[i].role == "agent" {
				lastContent = m.messages[i].content
				break
			}
		}
		if lastContent == "" {
			m.messages = append(m.messages, chatMessage{role: "system", content: "Nothing to copy."})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}
		if err := clipboard.WriteAll(lastContent); err != nil {
			m.messages = append(m.messages, chatMessage{role: "system", content: fmt.Sprintf("clipboard: %v", err)})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}
		m.messages = append(m.messages, chatMessage{role: "system", content: "Copied all."})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	var allBlocks []string
	for _, r := range m.messages {
		if r.role == "assistant" || r.role == "agent" {
			allBlocks = append(allBlocks, extractCodeBlocks(r.content)...)
		}
	}
	if len(allBlocks) == 0 {
		m.messages = append(m.messages, chatMessage{role: "system", content: "No code block found."})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	idx := 0
	if arg != "" {
		n, err := fmt.Sscanf(arg, "%d", &idx)
		if err != nil || n != 1 {
			m.messages = append(m.messages, chatMessage{role: "system", content: "Usage: /cp [N|all]"})
			m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
			m.updateViewportContent()
			return m, nil
		}
	}
	if idx < 0 || idx >= len(allBlocks) {
		m.messages = append(m.messages, chatMessage{role: "system", content: fmt.Sprintf("Only %d code blocks.", len(allBlocks))})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}

	if err := clipboard.WriteAll(allBlocks[idx]); err != nil {
		m.messages = append(m.messages, chatMessage{role: "system", content: fmt.Sprintf("clipboard: %v", err)})
		m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
		m.updateViewportContent()
		return m, nil
	}
	m.messages = append(m.messages, chatMessage{role: "system", content: fmt.Sprintf("Copied block %d.", idx)})
	m.rendered = append(m.rendered, m.renderMessage(m.messages[len(m.messages)-1]))
	m.updateViewportContent()
	m.vp.GotoBottom()
	return m, nil
}

var fenceRx = regexp.MustCompile("```[a-zA-Z0-9_+-]*\n?([\\s\\S]*?)```")

func extractCodeBlocks(s string) []string {
	matches := fenceRx.FindAllStringSubmatch(s, -1)
	var blocks []string
	for _, m := range matches {
		blocks = append(blocks, m[1])
	}
	return blocks
}

func addCopyLabels(s string, startIdx int) string {
	var result strings.Builder
	rest := s
	idx := startIdx
	for {
		loc := fenceRx.FindStringIndex(rest)
		if loc == nil {
			result.WriteString(rest)
			break
		}
		result.WriteString(rest[:loc[0]])
		result.WriteString(fmt.Sprintf("**[Copy %d]**\n", idx))
		result.WriteString(rest[loc[0]:loc[1]])
		rest = rest[loc[1]:]
		idx++
	}
	return result.String()
}

func resolvePath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = home + p[1:]
	}
	if len(p) > 0 && p[0] == '/' {
		return p, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if cwd[len(cwd)-1] != '/' {
		cwd += "/"
	}
	return cwd + p, nil
}

func (m *model) startStream(input string) tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	baseURL := m.activeProvider.BaseURL
	apiKey := m.activeProvider.APIKey
	modelID := m.activeModel

	sessMsgs := m.currentSession.Messages
	summary := m.currentSession.Summary
	var msgs []llm.Message

	if m.ctxMgr.NeedsSummary(sessMsgs) {
		needsNewSummary := summary == "" || m.currentSession.LastSummaryIdx < len(sessMsgs)-m.ctxMgr.WindowSize
		if needsNewSummary {
			prompt := m.ctxMgr.BuildSummaryPrompt(sessMsgs, summary, m.currentSession.LastSummaryIdx)
			newSummary, err := llm.Chat(baseURL, apiKey, modelID, prompt)
			if err == nil && newSummary != "" {
				summary = newSummary
				m.currentSession.Summary = newSummary
				m.currentSession.SummaryTokens = session.EstimateTokens(newSummary)
				m.currentSession.LastSummaryIdx = len(sessMsgs) - m.ctxMgr.WindowSize
				if m.currentSession.LastSummaryIdx < 0 {
					m.currentSession.LastSummaryIdx = 0
				}
				m.currentSession.Save()
			}
		}
		msgs = m.ctxMgr.PrepareMessages(sessMsgs, summary)
	} else {
		msgs = m.ctxMgr.PrepareMessages(sessMsgs, "")
	}

	if prompt, ok := cavemanPrompts[m.currentSession.CavemanMode]; ok {
		msgs = append([]llm.Message{{Role: "system", Content: prompt}}, msgs...)
	}

	m.ctxTokens = 0
	for _, msg := range msgs {
		m.ctxTokens += session.EstimateTokens(msg.Content)
	}

	sub := make(chan tea.Msg, 128)
	m.sub = sub

	go func() {
		defer cancel()
		err := llm.StreamChat(
			baseURL, apiKey, modelID, msgs,
			func(chunk string, done bool, err error) {
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					select {
					case sub <- errMsg{err: err}:
					case <-ctx.Done():
					}
					return
				}
				if done {
					select {
					case sub <- streamDoneMsg{}:
					case <-ctx.Done():
					}
					return
				}
				select {
				case sub <- streamProgressMsg(chunk):
				case <-ctx.Done():
				}
			},
		)
		if err != nil {
			select {
			case sub <- errMsg{err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return m.waitForStream()
}

func (m *model) waitForStream() tea.Cmd {
	return func() tea.Msg {
		return <-m.sub
	}
}

func (m *model) invalidateCache() {
	m.rendered = make([]string, len(m.messages))
	for i, msg := range m.messages {
		m.rendered[i] = m.renderMessage(msg)
	}
}

func (m *model) updateViewportContent() {
	var b strings.Builder
	for _, r := range m.rendered {
		b.WriteString(r)
		b.WriteString("\n\n")
	}
	if m.currentResp != "" {
		b.WriteString(m.renderStreaming())
	} else if m.loading {
		b.WriteString(m.renderLoading())
	}
	m.vp.SetContent(b.String())
}

func (m *model) glamourWidth() int {
	w := m.width - 4
	if w < 40 {
		w = 40
	}
	return w
}

func (m *model) renderMessage(msg chatMessage) string {
	switch msg.role {
	case "user":
		tok := session.EstimateTokens(msg.content)
		header := lipgloss.NewStyle().Bold(true).Foreground(userColor).Render(fmt.Sprintf("▎ You [%d tok]", tok))
		body := msg.content
		if r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(m.glamourWidth())); err == nil {
			if rendered, err := r.Render(msg.content); err == nil {
				body = rendered
			}
		}
		return header + "\n" + body

	case "assistant":
		tok := session.EstimateTokens(msg.content)
		header := lipgloss.NewStyle().Bold(true).Foreground(assistantColor).Render(fmt.Sprintf("▎ %s [%d tok]", m.activeModel, tok))
		offset := 0
		for _, r := range m.messages {
			if r.role == "assistant" && r.content == msg.content {
				break
			}
			if r.role == "assistant" {
				offset += len(extractCodeBlocks(r.content))
			}
		}
		body := addCopyLabels(msg.content, offset)
		if r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(m.glamourWidth())); err == nil {
			if rendered, err := r.Render(body); err == nil {
				body = rendered
			}
		}
		return header + "\n" + body

	case "system":
		return lipgloss.NewStyle().Italic(true).Foreground(systemColor).Render(msg.content)

	default:
		return msg.content
	}
}

func (m *model) renderStreaming() string {
	header := lipgloss.NewStyle().Bold(true).Foreground(assistantColor).Render("▎ " + m.activeModel)
	offset := 0
	for _, r := range m.messages {
		if r.role == "assistant" {
			offset += len(extractCodeBlocks(r.content))
		}
	}
	content := addCopyLabels(m.currentResp, offset)
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(m.glamourWidth()))
	if err != nil {
		return header + "\n" + content + "\n" + m.spinner.View()
	}
	rendered, err := r.Render(content)
	if err != nil {
		return header + "\n" + content + "\n" + m.spinner.View()
	}
	return header + "\n" + rendered + "\n" + m.spinner.View()
}

func (m *model) renderLoading() string {
	header := lipgloss.NewStyle().Bold(true).Foreground(assistantColor).Render("▎ " + m.activeModel)
	return header + "\n" + m.spinner.View() + " Thinking..."
}

func (m *model) renderStatusBar() string {
	w := m.width
	if w == 0 {
		w = 80
	}
	modelStr := lipgloss.NewStyle().Foreground(assistantColor).Render(" " + m.activeModel + " ")
	hasSummary := m.currentSession.Summary != ""
	var summaryTag string
	if hasSummary {
		summaryTag = " sum"
	}
	cavMode := m.currentSession.CavemanMode
	var cavTag string
	if cavMode != "" {
		cavTag = " cav:" + cavMode
	}
	tokens := lipgloss.NewStyle().Foreground(systemColor).Render(fmt.Sprintf(" %din/%dout  ctx %dtok%s%s ", m.inputTokens, m.outputTokens, m.ctxTokens, summaryTag, cavTag))
	mid := lipgloss.NewStyle().Foreground(systemColor).Render(fmt.Sprintf(" %d msgs ", len(m.messages)))
	left := mid + tokens
	spaces := w - lipgloss.Width(left) - lipgloss.Width(modelStr)
	if spaces < 0 {
		spaces = 0
	}
	bar := lipgloss.NewStyle().Background(statusBg).Width(w).Render(
		left + strings.Repeat(" ", spaces) + modelStr,
	)
	return bar
}

func (m *model) renderSuggestionsOverlay() string {
	if !m.showSuggestions || len(m.suggestions) == 0 {
		return ""
	}
	var b strings.Builder
	maxN := len(m.suggestions)
	if maxN > 4 {
		maxN = 4
	}
	for i := 0; i < maxN; i++ {
		cmd := m.suggestions[i]
		var desc string
		for _, c := range commands {
			if c.cmd == cmd {
				desc = c.desc
				break
			}
		}
		if i == m.suggestionIdx {
			b.WriteString(lipgloss.NewStyle().
				Background(suggestSelBg).
				Foreground(suggestText).
				Render(fmt.Sprintf("  %s  %s", cmd, desc)))
		} else {
			b.WriteString(lipgloss.NewStyle().
				Background(suggestBg).
				Foreground(suggestDesc).
				Render(fmt.Sprintf("  %s  %s", cmd, desc)))
		}
		if i < maxN-1 {
			b.WriteString("\n")
		}
	}
	return b.String() + "\n"
}

func (m *model) View() tea.View {
	if !m.ready {
		v := tea.NewView("\n  Loading...")
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	if m.state == modelListState {
		s := lipgloss.NewStyle().Padding(1, 2)
		v := tea.NewView(s.Render(m.mdList.View()))
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	if m.state == sessionListState {
		listView := lipgloss.NewStyle().Padding(1, 2).Render(m.mdList.View())
		if m.renameTarget != nil {
			prompt := lipgloss.NewStyle().Foreground(systemColor).Render("Rename: ")
			content := listView + "\n" + prompt + m.input.View()
			v := tea.NewView(content)
			v.AltScreen = true
			v.MouseMode = tea.MouseModeCellMotion
			return v
		}
		if m.filterText != "" {
			filterLine := lipgloss.NewStyle().
				Background(lipgloss.Color("#313244")).
				Foreground(lipgloss.Color("#CDD6F4")).
				Padding(0, 2).
				Render("filter: " + m.filterText + "▎")
			content := listView + "\n" + filterLine
			v := tea.NewView(content)
			v.AltScreen = true
			v.MouseMode = tea.MouseModeCellMotion
			return v
		}
		v := tea.NewView(listView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	suggest := m.renderSuggestionsOverlay()
	content := fmt.Sprintf("%s\n%s\n%s%s", m.vp.View(), m.renderStatusBar(), suggest, m.input.View())
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
