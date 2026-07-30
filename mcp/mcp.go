package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"

	"tuichat/llm"
)

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	dec    *json.Decoder
	stderr io.ReadCloser
	nextID int64
}

func NewClient(command string, args []string) (*Client, error) {
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		dec:    json.NewDecoder(stdout),
		stderr: stderr,
	}
	if err := c.handshake(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) handshake() error {
	var serverResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.Call("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "tuichat",
			"version": "1.0",
		},
	}, &serverResult); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// send initialized notification (no id)
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	notif = append(notif, '\n')
	c.stdin.Write(notif)
	return nil
}

func (c *Client) Close() {
	c.cmd.Process.Kill()
	c.cmd.Wait()
}

func (c *Client) Stderr() string {
	b, _ := io.ReadAll(c.stderr)
	if len(b) > 0 {
		return string(b)
	}
	return ""
}

func (c *Client) Call(method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&c.nextID, 1)
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	for {
		var resp struct {
			ID     int64            `json:"id"`
			Result json.RawMessage  `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := c.dec.Decode(&resp); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if resp.ID == id {
			if resp.Error != nil {
				return fmt.Errorf("MCP %d: %s", resp.Error.Code, resp.Error.Message)
			}
			if result != nil && len(resp.Result) > 0 {
				return json.Unmarshal(resp.Result, result)
			}
			return nil
		}
	}
}

func (c *Client) ListTools() ([]Tool, error) {
	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.Call("tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

func (c *Client) CallTool(name string, args map[string]interface{}) (string, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": args,
	}
	var result struct {
		Content []ContentItem `json:"content"`
		IsError bool          `json:"isError"`
	}
	if err := c.Call("tools/call", params, &result); err != nil {
		return "", err
	}
	var b string
	for _, item := range result.Content {
		if item.Type == "text" {
			if b != "" {
				b += "\n"
			}
			b += item.Text
		}
	}
	if result.IsError {
		return "", fmt.Errorf("tool error: %s", b)
	}
	return b, nil
}

func ToolsToLLM(tools []Tool) []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, len(tools))
	for i, t := range tools {
		params := t.InputSchema
		if params == nil {
			params = map[string]interface{}{"type": "object"}
		}
		defs[i] = llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolDefFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return defs
}
