package context

import (
	"fmt"
	"strings"

	"tuichat/llm"
	"tuichat/session"
)

type Manager struct {
	Budget     int
	WindowSize int
}

func New(budget, windowSize int) *Manager {
	if budget <= 0 {
		budget = 40000
	}
	if windowSize <= 0 {
		windowSize = 20
	}
	return &Manager{Budget: budget, WindowSize: windowSize}
}

func (m *Manager) NeedsSummary(messages []session.Message) bool {
	total := 0
	for _, msg := range messages {
		total += msg.Tokens
	}
	return total > m.Budget
}

func (m *Manager) BuildSummaryPrompt(messages []session.Message, existingSummary string, coveredIdx int) []llm.Message {
	var b strings.Builder
	b.WriteString("Summarize the following chat conversation concisely. ")
	b.WriteString("Keep all important facts, decisions, user preferences, requirements, and technical context. ")
	b.WriteString("Use bullet points. The summary will be injected as context for future responses.\n\n")

	start := 0
	if existingSummary != "" {
		b.WriteString("## Existing Summary\n")
		b.WriteString(existingSummary)
		b.WriteString("\n\n## New Messages to Incorporate\n")
		start = coveredIdx
	}

	end := len(messages) - m.WindowSize
	if end < start {
		end = start
	}
	if end > len(messages) {
		end = len(messages)
	}

	for _, msg := range messages[start:end] {
		b.WriteString(fmt.Sprintf("**%s**: %s\n", msg.Role, msg.Content))
	}

	return []llm.Message{{Role: "user", Content: b.String()}}
}

func (m *Manager) PrepareMessages(messages []session.Message, summary string) []llm.Message {
	if summary != "" && m.NeedsSummary(messages) {
		start := len(messages) - m.WindowSize
		if start < 0 {
			start = 0
		}
		msgs := make([]llm.Message, 0, m.WindowSize+1)
		msgs = append(msgs, llm.Message{
			Role:    "system",
			Content: "Prior conversation summary: " + summary,
		})
		for _, msg := range messages[start:] {
			msgs = append(msgs, llm.Message{Role: msg.Role, Content: msg.Content})
		}
		return msgs
	}

	msgs := make([]llm.Message, len(messages))
	for i, msg := range messages {
		msgs[i] = llm.Message{Role: msg.Role, Content: msg.Content}
	}
	return msgs
}
