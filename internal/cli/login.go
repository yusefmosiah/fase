package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/cogent/internal/adapters/native"
	"github.com/yusefmosiah/cogent/internal/service"
)

func newLoginCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Run adapter auth flows and inspect auth status",
	}

	cmd.AddCommand(
		newLoginChatGPTCommand(root),
		newLoginStatusCommand(root),
	)

	return cmd
}

func newLoginChatGPTCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "chatgpt",
		Short: "Sign in via Codex OAuth for the native ChatGPT provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := serviceOpen(root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			binary := "codex"
			if cfg, ok := svc.Config.Adapters.ByName("native"); ok && strings.TrimSpace(cfg.Binary) != "" {
				binary = cfg.Binary
			}
			login := exec.CommandContext(cmd.Context(), binary, "login")
			login.Stdin = cmd.InOrStdin()
			login.Stdout = cmd.OutOrStdout()
			login.Stderr = cmd.ErrOrStderr()
			return login.Run()
		},
	}
}

func newLoginStatusCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show auth state for the native ChatGPT provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := serviceOpen(root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			binary := "codex"
			if cfg, ok := svc.Config.Adapters.ByName("native"); ok && strings.TrimSpace(cfg.Binary) != "" {
				binary = cfg.Binary
			}

			statusOutput, statusErr := exec.CommandContext(cmd.Context(), binary, "login", "status").CombinedOutput()
			auth := native.NewChatGPTAuth(native.ChatGPTAuthOptions{})
			header, tokenErr := auth(context.Background())
			result := map[string]any{
				"provider":            "chatgpt",
				"codex_binary":        binary,
				"codex_status_output": strings.TrimSpace(string(statusOutput)),
				"codex_status_ok":     statusErr == nil,
				"auth_file_ok":        tokenErr == nil,
			}
			if tokenErr == nil {
				result["authorization_header"] = header
			} else {
				result["auth_file_error"] = tokenErr.Error()
			}
			if statusErr != nil {
				result["codex_status_error"] = statusErr.Error()
			}

			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), result)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "provider: chatgpt\n")
			fmt.Fprintf(cmd.OutOrStdout(), "codex binary: %s\n", binary)
			fmt.Fprintf(cmd.OutOrStdout(), "codex login status: ")
			if statusErr == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "ok\n")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "error\n")
			}
			if text := strings.TrimSpace(string(statusOutput)); text != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", text)
			}
			if tokenErr == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "auth file: ok\n")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "auth file: %v\n", tokenErr)
			}
			return nil
		},
	}
}

func serviceOpen(configPath string) (*service.Service, error) {
	return service.Open(context.Background(), configPath)
}
