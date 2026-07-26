package tui

import (
	"context"
	"fmt"
	"strings"

	"tuichat/config"
	"tuichat/llm"
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
type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

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
	suggestions     []string
	suggestionIdx   int
	showSuggestions bool
	inputTokens     int
	outputTokens    int
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
	{"/new", "New session"},
	{"/exit", "Quit"},
}

func New(cfg *config.Config) *model {
	ti := textinput.New()
	ti.Placeholder = "Type a message... (/model, /new, /sessions, /reset, /exit)"
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
	m := &model{
		cfg:            cfg,
		input:          ti,
		mdList:         l,
		activeModel:    activeModel,
		activeProvider: activeProvider,
		sub:            make(chan tea.Msg, 128),
		spinner:        s,
	}
	m.currentSession = session.New(activeModel)
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
		m.syncViewport()
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
		return m, cmd

	case streamProgressMsg:
		m.currentResp += string(msg)
		m.syncViewport()
		m.vp.GotoBottom()
		return m, m.waitForStream()

	case streamDoneMsg:
		m.messages = append(m.messages, chatMessage{role: "assistant", content: m.currentResp})
		m.outputTokens += session.EstimateTokens(m.currentResp)
		m.currentSession.AddMessage("assistant", m.currentResp)
		m.currentSession.Model = m.activeModel
		if m.currentSession.Name == "Untitled" {
			m.currentSession.Name = m.currentSession.GenerateName()
		}
		m.currentSession.Save()
		m.currentResp = ""
		m.loading = false
		m.syncViewport()
		m.vp.GotoBottom()
		return m, nil

	case errMsg:
		m.messages = append(m.messages, chatMessage{
			role:    "system",
			content: fmt.Sprintf("Error: %v", msg.err),
		})
		m.loading = false
		m.currentResp = ""
		m.syncViewport()
		m.vp.GotoBottom()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.loading {
			m.syncViewport()
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
			m.syncViewport()
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
		m.inputTokens += session.EstimateTokens(input)
		m.currentSession.AddMessage("user", input)
		m.currentSession.Save()
		m.loading = true
		m.currentResp = ""
		m.syncViewport()
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
				return m, vpCmd
			}
			m.input.SetValue(m.history[m.historyIdx])
			m.input.CursorEnd()
			return m, nil
		}
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
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
		m.state = chatState
		m.input.Focus()
		return m, nil

	case "r":
		selected, ok := m.mdList.SelectedItem().(sessionItem)
		if ok {
			m.input.SetValue(selected.sess.Name)
			m.input.Focus()
			m.state = chatState
			return m, nil
		}
		return m, nil

	case "d", "ctrl+d":
		selected, ok := m.mdList.SelectedItem().(sessionItem)
		if ok {
			selected.sess.Delete()
			m.refreshSessionList()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.mdList, cmd = m.mdList.Update(msg)
	return m, cmd
}

func (m *model) loadSession(s *session.Session) {
	m.currentSession = s
	m.messages = toChatMessages(s)
	m.inputTokens = countTokens(s.Messages, "user")
	m.outputTokens = countTokens(s.Messages, "assistant")
	m.activeModel = s.Model
	m.loading = false
	m.currentResp = ""
	m.syncViewport()
	m.vp.GotoBottom()
}

func (m *model) refreshSessionList() {
	sessions, _ := session.List()
	m.mdList.SetItems(buildSessionItems(sessions))
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
		m.currentResp = ""
		m.loading = false
		m.inputTokens = 0
		m.outputTokens = 0
		m.currentSession = session.New(m.activeModel)
		m.syncViewport()
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
			m.syncViewport()
			return m, nil
		}
		m.state = sessionListState
		m.mdList.SetItems(buildSessionItems(sessions))
		m.mdList.ResetSelected()
		m.mdList.Title = "Sessions (r=rename d=delete)"
		m.input.Blur()
		return m, nil

	case strings.HasPrefix(input, "/new "):
		name := strings.TrimSpace(strings.TrimPrefix(input, "/new "))
		if name == "" {
			m.messages = append(m.messages, chatMessage{
				role: "system", content: "Usage: /new <session name>",
			})
			m.syncViewport()
			return m, nil
		}
		if len(m.messages) > 0 {
			m.currentSession.Save()
		}
		m.messages = nil
		m.currentResp = ""
		m.loading = false
		m.inputTokens = 0
		m.outputTokens = 0
		m.currentSession = session.New(m.activeModel)
		m.currentSession.Name = name
		m.syncViewport()
		return m, nil

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
		m.syncViewport()
		return m, nil
	}
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

	msgs := make([]llm.Message, 0, len(m.messages))
	for _, msg := range m.messages {
		if msg.role == "system" {
			continue
		}
		msgs = append(msgs, llm.Message{Role: msg.role, Content: msg.content})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: input})

	sub := make(chan tea.Msg, 128)
	m.sub = sub

	go func() {
		defer cancel()
		err := llm.StreamChat(
			baseURL, apiKey, modelID, msgs,
			func(chunk string, done bool, err error) {
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

func (m *model) syncViewport() {
	var b strings.Builder
	for _, msg := range m.messages {
		b.WriteString(m.renderMessage(msg))
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
		return header + "\n" + msg.content

	case "assistant":
		tok := session.EstimateTokens(msg.content)
		header := lipgloss.NewStyle().Bold(true).Foreground(assistantColor).Render(fmt.Sprintf("▎ %s [%d tok]", m.activeModel, tok))
		r, err := glamour.NewTermRenderer(glamour.WithWordWrap(m.glamourWidth()))
		if err != nil {
			return header + "\n" + msg.content
		}
		rendered, err := r.Render(msg.content)
		if err != nil {
			return header + "\n" + msg.content
		}
		return header + "\n" + rendered

	case "system":
		return lipgloss.NewStyle().Italic(true).Foreground(systemColor).Render(msg.content)

	default:
		return msg.content
	}
}

func (m *model) renderStreaming() string {
	header := lipgloss.NewStyle().Bold(true).Foreground(assistantColor).Render("▎ " + m.activeModel)
	r, err := glamour.NewTermRenderer(glamour.WithWordWrap(m.glamourWidth()))
	if err != nil {
		return header + "\n" + m.currentResp + "\n" + m.spinner.View()
	}
	rendered, err := r.Render(m.currentResp)
	if err != nil {
		return header + "\n" + m.currentResp + "\n" + m.spinner.View()
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
	totalTok := m.inputTokens + m.outputTokens
	tokens := lipgloss.NewStyle().Foreground(systemColor).Render(fmt.Sprintf(" %din/%dout/%dtotal ", m.inputTokens, m.outputTokens, totalTok))
	mid := lipgloss.NewStyle().Foreground(systemColor).Render(fmt.Sprintf(" %d msgs ", len(m.messages)))
	spaces := w - lipgloss.Width(modelStr) - lipgloss.Width(tokens) - lipgloss.Width(mid)
	if spaces < 0 {
		spaces = 0
	}
	bar := lipgloss.NewStyle().Background(statusBg).Width(w).Render(
		modelStr + strings.Repeat(" ", spaces) + mid + tokens,
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

	if m.state == modelListState || m.state == sessionListState {
		s := lipgloss.NewStyle().Padding(1, 2)
		v := tea.NewView(s.Render(m.mdList.View()))
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
