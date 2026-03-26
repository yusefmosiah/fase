package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yusefmosiah/cogent/internal/channelmeta"
	"github.com/yusefmosiah/cogent/internal/core"
)

func newCheckCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Manage check records (verification results for work items)",
	}

	cmd.AddCommand(
		newCheckCreateCommand(root),
		newCheckListCommand(root),
		newCheckShowCommand(root),
	)

	return cmd
}

func newCheckCreateCommand(root *rootOptions) *cobra.Command {
	var result string
	var notes string
	var checkerModel string
	var workerModel string
	var buildOK bool
	var testsPassed int
	var testsFailed int
	var testOutput string
	var diffStat string
	var screenshots string
	var videos string

	cmd := &cobra.Command{
		Use:   "create <work-id>",
		Short: "Create a check record for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			workID := args[0]
			body := map[string]any{
				"work_id":       workID,
				"result":        result,
				"checker_model": checkerModel,
				"worker_model":  workerModel,
				"report": map[string]any{
					"build_ok":      buildOK,
					"tests_passed":  testsPassed,
					"tests_failed":  testsFailed,
					"test_output":   testOutput,
					"diff_stat":     diffStat,
					"screenshots":   splitCSV(screenshots),
					"videos":        splitCSV(videos),
					"checker_notes": notes,
				},
			}
			data, err := c.doPost("/api/check/create", body)
			if err != nil {
				return err
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var resp map[string]any
			if err := json.Unmarshal(data, &resp); err != nil {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "check %s: %s\n", resp["check_id"], result)
			return nil
		},
	}

	cmd.Flags().StringVar(&result, "result", "", "check result: pass or fail (required)")
	cmd.Flags().StringVar(&notes, "notes", "", "checker observations")
	cmd.Flags().StringVar(&checkerModel, "checker-model", "", "model that ran the check")
	cmd.Flags().StringVar(&workerModel, "worker-model", "", "model that did the implementation")
	cmd.Flags().BoolVar(&buildOK, "build-ok", false, "did the build succeed")
	cmd.Flags().IntVar(&testsPassed, "tests-passed", 0, "number of tests passed")
	cmd.Flags().IntVar(&testsFailed, "tests-failed", 0, "number of tests failed")
	cmd.Flags().StringVar(&testOutput, "test-output", "", "test output (truncated)")
	cmd.Flags().StringVar(&diffStat, "diff-stat", "", "git diff stat")
	cmd.Flags().StringVar(&screenshots, "screenshots", "", "comma-separated screenshot paths")
	cmd.Flags().StringVar(&videos, "videos", "", "comma-separated video paths")
	_ = cmd.MarkFlagRequired("result")

	return cmd
}

func newCheckListCommand(root *rootOptions) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "list <work-id>",
		Short: "List check records for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			params := url.Values{}
			params.Set("work_id", args[0])
			params.Set("limit", strconv.Itoa(limit))
			data, err := c.doGet("/api/check/list", params)
			if err != nil {
				return err
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var checks []map[string]any
			if err := json.Unmarshal(data, &checks); err != nil {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			for _, ch := range checks {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n",
					ch["check_id"], ch["result"],
					strings.TrimSpace(fmt.Sprintf("%v", ch["checker_model"])))
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", core.DefaultCheckRecordListLimit, "max check records to return")
	return cmd
}

func newCheckShowCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "show <check-id>",
		Short: "Show a check record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			params := url.Values{}
			params.Set("check_id", args[0])
			data, err := c.doGet("/api/check/show", params)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newReportCommand(_ *rootOptions) *cobra.Command {
	var msgType string
	cmd := &cobra.Command{
		Use:   "report [message]",
		Short: "Report status to supervisor or host",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			msg := strings.Join(args, " ")
			msgType = channelmeta.NormalizeWorkerReportType(msgType)
			data, err := c.doPost("/api/channel/send", map[string]any{
				"content": msg,
				"meta":    channelmeta.WorkerReportMeta(msgType),
			})
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVar(&msgType, "type", "info", "message type: info, status_update, escalation")
	return cmd
}
