package cli

import (
	"context"
	"fmt"
	"net/http"

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
		RunE: func(cmd *cobra.Command, args []string) error {
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

			server := mcpserver.New(svc)
			handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
				return server.MCP
			}, nil)

			fmt.Fprintf(cmd.OutOrStdout(), "FASE MCP server listening on %s\n", httpAddr)
			return http.ListenAndServe(httpAddr, handler)
		},
	}
	httpCmd.Flags().StringVar(&httpAddr, "addr", ":4243", "HTTP listen address")

	cmd.AddCommand(stdioCmd, httpCmd)
	return cmd
}
