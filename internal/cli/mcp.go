package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/yusefmosiah/fase/internal/mcpserver"
	"github.com/yusefmosiah/fase/internal/service"
)

func newMCPCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run fase as an MCP server",
	}

	stdioCmd := &cobra.Command{
		Use:   "stdio",
		Short: "Run MCP server over stdio (for Claude Code and other MCP clients)",
		Long: `WARNING: 'fase mcp stdio' opens the database directly. If 'fase serve' is
also running, this creates concurrent writers which can corrupt the database.
Use 'fase mcp proxy' instead — it routes through serve's HTTP API.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Warn if serve is already running — concurrent DB access causes corruption.
			if info, err := loadServeInfo(); err == nil {
				fmt.Fprintf(os.Stderr, "WARNING: fase serve is running (pid %d, port %d). Using 'fase mcp stdio' "+
					"alongside serve risks database corruption. Use 'fase mcp proxy' instead.\n", info.PID, info.Port)
			}
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			server := mcpserver.New(svc)
			return server.RunStdio(cmd.Context())
		},
	}

	var httpAddr string
	httpCmd := &cobra.Command{
		Use:   "http",
		Short: "Run MCP server over HTTP streaming",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			// Use SessionManager for per-session server isolation (VAL-SUPERVISOR-003).
			// This ensures external MCP traffic doesn't share mutable state with supervisor.
			sessionManager := mcpserver.NewSessionManager(svc)
			handler := mcp.NewStreamableHTTPHandler(sessionManager.GetServerForRequest, nil)

			fmt.Fprintf(cmd.OutOrStdout(), "FASE MCP server listening on %s\n", httpAddr)
			return http.ListenAndServe(httpAddr, handler)
		},
	}
	httpCmd.Flags().StringVar(&httpAddr, "addr", ":4243", "HTTP listen address")

	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Proxy MCP stdio to the running fase serve HTTP endpoint",
		Long: `Reads serve.json to find the running fase serve port, then proxies
MCP requests from stdin to serve's /mcp endpoint over HTTP. This avoids
the WAL split problem where a separate DB connection sees stale data.

Use this in .mcp.json instead of 'fase mcp stdio'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := loadServeInfo()
			if err != nil {
				return fmt.Errorf("fase serve is not running: %w", err)
			}
			baseURL := fmt.Sprintf("http://localhost:%d/mcp", info.Port)
			return runMCPProxy(cmd.Context(), baseURL)
		},
	}

	cmd.AddCommand(stdioCmd, httpCmd, proxyCmd)
	return cmd
}

// runMCPProxy proxies MCP JSON-RPC between stdio and serve's HTTP endpoint.
// Tool calls: stdin → HTTP POST → stdout (SSE events parsed and relayed).
// Channel notifications: WebSocket /ws → stdout (push from serve to client).
// proxyStdoutMu synchronizes writes to stdout from the main goroutine
// (MCP responses) and the WebSocket goroutine (channel notifications).
var proxyStdoutMu sync.Mutex

func runMCPProxy(ctx context.Context, endpoint string) error {
	client := &http.Client{Timeout: 5 * time.Minute}

	// Derive WebSocket URL from MCP endpoint for channel notifications.
	wsURL := strings.Replace(strings.Replace(endpoint, "/mcp", "/ws", 1), "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	// Start channel notification listener (serve → stdout).
	go proxyChannelNotifications(ctx, wsURL)

	// Proxy tool calls (stdin → HTTP → stdout).
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var sessionID string

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(line))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("proxy request: %w", err)
		}

		// Capture session ID from response for subsequent requests.
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			sessionID = sid
		}

		// Parse SSE response and relay as raw JSON lines.
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			relaySSEToStdout(resp.Body) // locks per-line, not for entire stream
		} else {
			proxyStdoutMu.Lock()
			body, _ := io.ReadAll(resp.Body)
			if len(body) > 0 {
				os.Stdout.Write(body)
				fmt.Fprintln(os.Stdout)
			}
			proxyStdoutMu.Unlock()
		}
		resp.Body.Close()
	}
	return scanner.Err()
}

// relaySSEToStdout reads SSE events and writes the data lines to stdout.
// Uses per-line locking so channel notifications aren't blocked during long streams.
func relaySSEToStdout(body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024) // 10MB buffer
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := line[6:]
			if data != "" && data != "[DONE]" {
				proxyStdoutMu.Lock()
				fmt.Fprintln(os.Stdout, data)
				proxyStdoutMu.Unlock()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "[mcp-proxy] SSE scanner error: %v\n", err)
	}
}

// proxyChannelNotifications connects to serve's WebSocket and relays
// channel_message events as JSON-RPC notifications to stdout.
func proxyChannelNotifications(ctx context.Context, wsURL string) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := listenWebSocketChannels(ctx, wsURL)
		if err != nil && ctx.Err() == nil {
			// Reconnect after brief delay.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// listenWebSocketChannels opens a raw WebSocket to serve and relays
// channel_message events as notifications/claude/channel to stdout.
func listenWebSocketChannels(ctx context.Context, wsURL string) error {
	// Minimal WebSocket client — just enough for text frames from serve.
	dialer := &net.Dialer{}
	u, err := url.Parse(wsURL)
	if err != nil {
		return err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return err
	}
	defer conn.Close()

	// WebSocket upgrade handshake.
	key := "dGhlIHNhbXBsZSBub25jZQ==" // static key, fine for local
	upgradeReq := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		u.RequestURI(), u.Host, key)
	if _, err := conn.Write([]byte(upgradeReq)); err != nil {
		return err
	}

	// Read upgrade response (discard).
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			break // end of headers
		}
	}

	// Read WebSocket frames.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Read frame header (simplified — text frames only, no fragmentation).
		header := make([]byte, 2)
		if _, err := io.ReadFull(br, header); err != nil {
			return err
		}
		masked := header[1]&0x80 != 0
		payloadLen := int(header[1] & 0x7F)
		switch payloadLen {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(br, ext); err != nil {
				return err
			}
			payloadLen = int(ext[0])<<8 | int(ext[1])
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(br, ext); err != nil {
				return err
			}
			payloadLen = int(ext[4])<<24 | int(ext[5])<<16 | int(ext[6])<<8 | int(ext[7])
		}

		var maskKey [4]byte
		if masked {
			if _, err := io.ReadFull(br, maskKey[:]); err != nil {
				return err
			}
		}

		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}

		// Parse the WebSocket message as a serve broadcast event.
		var wsEvent struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &wsEvent); err != nil {
			continue
		}

		if wsEvent.Type == "channel_message" {
			// Extract content and relay as claude/channel notification.
			var data struct {
				Content string            `json:"content"`
				Meta    map[string]string `json:"meta,omitempty"`
			}
			if err := json.Unmarshal(wsEvent.Data, &data); err != nil {
				continue
			}

			notification := map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/claude/channel",
				"params": map[string]any{
					"content": data.Content,
					"meta":    data.Meta,
				},
			}
			notifJSON, _ := json.Marshal(notification)
			proxyStdoutMu.Lock()
			fmt.Fprintln(os.Stdout, string(notifJSON))
			proxyStdoutMu.Unlock()
		}
	}
}
