package tui

import (
	"context"
	"fmt"
	"strings"

	"tuichat/config"
	"tuichat/llm"

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

type model struct {
	state          state
	cfg            *config.Config
	messages       []chatMessage
	input          textinput.Model
	vp             viewport.Model
	mdList         list.Model
	activeModel    string
	activeProvider *config.Provider
	loading        bool
	currentResp    string
	sub            chan tea.Msg
	ready          bool
	width          int
	spinner        spinner.Model
	cancel         context.CancelFunc
	history        []string
	historyIdx     int
	savedInput     string
}

var (
	userColor      = lipgloss.Color("#7D56F4")
	assistantColor = lipgloss.Color("#00D68F")
	systemColor    = lipgloss.Color("#6B6B6B")
	statusBg       = lipgloss.Color("#2A2A2A")
)

func New(cfg *config.Config) *model {
	ti := textinput.New()
	ti.Placeholder = "Type a message... (/model, /reset, /exit)"
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
	items := buildListItems(cfg)
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select Model"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()

	return &model{
		cfg:            cfg,
		input:          ti,
		mdList:         l,
		activeModel:    activeModel,
		activeProvider: activeProvider,
		sub:            make(chan tea.Msg, 128),
		spinner:        s,
	}
}

func buildListItems(cfg *config.Config) []list.Item {
	var items []list.Item
	for i := range cfg.Providers {
		for _, m := range cfg.Providers[i].Models {
			items = append(items, modelItem{provider: &cfg.Providers[i], model: m})
		}
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
		m.vp = viewport.New(
			viewport.WithWidth(msg.Width),
			viewport.WithHeight(msg.Height-4),
		)
		m.vp.YPosition = 0
		m.input.SetWidth(msg.Width - 4)
		m.mdList.SetWidth(msg.Width)
		m.mdList.SetHeight(msg.Height - 4)
		m.ready = true
		return m, nil

	case tea.KeyPressMsg:
		if m.state == modelListState {
			return m.handleModelListKey(msg)
		}
		return m.handleChatKey(msg)

	case streamProgressMsg:
		m.currentResp += string(msg)
		m.syncViewport()
		m.vp.GotoBottom()
		return m, m.waitForStream()

	case streamDoneMsg:
		m.messages = append(m.messages, chatMessage{role: "assistant", content: m.currentResp})
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

func (m *model) handleChatKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Keystroke() {
	case "ctrl+c":
		return m, tea.Quit

	case "enter":
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
			return m.handleCommand(input)
		}
		if input == "" {
			return m, nil
		}

		m.history = append(m.history, input)
		m.historyIdx = -1
		m.savedInput = ""

		m.messages = append(m.messages, chatMessage{role: "user", content: input})
		m.loading = true
		m.currentResp = ""
		m.syncViewport()
		m.vp.GotoBottom()
		return m, m.startStream(input)

	case "up":
		if len(m.history) > 0 {
			if m.historyIdx == -1 {
				m.historyIdx = len(m.history) - 1
				m.savedInput = m.input.Value()
			} else if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input.SetValue(m.history[m.historyIdx])
			m.input.CursorEnd()
			return m, nil
		}
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		return m, vpCmd

	case "down":
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
	return m, cmd
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

func (m *model) handleCommand(input string) (tea.Model, tea.Cmd) {
	switch input {
	case "/exit":
		return m, tea.Quit

	case "/reset":
		if m.cancel != nil {
			m.cancel()
		}
		m.messages = nil
		m.currentResp = ""
		m.loading = false
		m.syncViewport()
		return m, nil

	case "/model", "/switch":
		m.state = modelListState
		m.mdList.SetItems(buildListItems(m.cfg))
		m.mdList.ResetSelected()
		m.input.Blur()
		return m, nil

	default:
		m.messages = append(m.messages, chatMessage{
			role:    "system",
			content: fmt.Sprintf("Unknown: %s\nAvailable: /model, /reset, /exit", input),
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

func (m *model) renderMessage(msg chatMessage) string {
	switch msg.role {
	case "user":
		header := lipgloss.NewStyle().Bold(true).Foreground(userColor).Render("▎ You")
		return header + "\n" + msg.content

	case "assistant":
		header := lipgloss.NewStyle().Bold(true).Foreground(assistantColor).Render("▎ " + m.activeModel)
		rendered, err := glamour.Render(msg.content, "dark")
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
	rendered, err := glamour.Render(m.currentResp, "dark")
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
	left := lipgloss.NewStyle().Foreground(assistantColor).Render(" " + m.activeModel + " ")
	count := lipgloss.NewStyle().Foreground(systemColor).Render(fmt.Sprintf(" %d msgs ", len(m.messages)))
	spaces := w - lipgloss.Width(left) - lipgloss.Width(count)
	if spaces < 0 {
		spaces = 0
	}
	bar := lipgloss.NewStyle().Background(statusBg).Width(w).Render(
		left + strings.Repeat(" ", spaces) + count,
	)
	return bar
}

func (m *model) View() tea.View {
	if !m.ready {
		v := tea.NewView("\n  Loading...")
		v.AltScreen = true
		return v
	}

	if m.state == modelListState {
		s := lipgloss.NewStyle().Padding(1, 2)
		v := tea.NewView(s.Render(m.mdList.View()))
		v.AltScreen = true
		return v
	}

	v := tea.NewView(fmt.Sprintf("%s\n%s\n%s", m.vp.View(), m.renderStatusBar(), m.input.View()))
	v.AltScreen = true
	return v
}
