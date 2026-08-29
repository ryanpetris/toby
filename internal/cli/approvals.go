package cli

// The approvals command: list pending launch approvals, describe one with an
// interactive decision prompt, or decide one directly.

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/version"
)

const (
	approvalsCommandName = "approvals"
	approvalsApproveWord = "approve"
	approvalsDenyWord    = "deny"
)

func isConfigFreeApprovalsInvocation(arguments []string) bool {
	flags := rootFlagValues{}
	root := &cobra.Command{
		Use:              "toby",
		Version:          version.String(),
		SilenceUsage:     true,
		SilenceErrors:    true,
		TraverseChildren: true,
		Run:              func(*cobra.Command, []string) {},
	}
	root.SetArgs(append([]string(nil), arguments...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	addRootPersistentFlags(root, &flags)

	command := &cobra.Command{
		Use:  approvalsCommandName,
		Args: cobra.MaximumNArgs(2),
		Run:  func(*cobra.Command, []string) {},
	}
	root.AddCommand(command)

	executed, err := root.ExecuteC()
	if err != nil && executed == nil {
		return false
	}
	return executed == command
}

func newApprovalsCommand(params Params) *cobra.Command {
	return &cobra.Command{
		Use:   approvalsCommandName + " [approval-id] [approve|deny]",
		Short: "List or decide pending launch approvals.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 2 {
				return fmt.Errorf("at most an approval id and a decision are accepted")
			}
			if len(args) == 2 &&
				args[1] != approvalsApproveWord &&
				args[1] != approvalsDenyWord {
				return fmt.Errorf(
					"decision must be %q or %q",
					approvalsApproveWord,
					approvalsDenyWord,
				)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if params.Agent == nil {
				return fmt.Errorf("agent client is not configured")
			}

			session, err := params.Agent.OpenAgent(cmd.Context(), nil)
			if err != nil {
				return exitcode.New(
					1,
					"agent is not running or unavailable: %v",
					err,
				)
			}
			defer func() {
				params.Diagnostics.Logger("cli.approvals").DebugError(
					"close agent session after approvals command",
					session.Close(),
				)
			}()

			switch len(args) {
			case 0:
				return listApprovals(cmd, session)
			case 1:
				return promptApproval(cmd, session, args[0])
			default:
				return decideApproval(
					cmd,
					session,
					args[0],
					args[1] == approvalsApproveWord,
				)
			}
		},
	}
}

func listApprovals(cmd *cobra.Command, session *client.AgentSession) error {
	listing, err := session.Approvals(cmd.Context())
	if err != nil {
		return err
	}
	reportUnreachableLaunches(cmd, listing.UnreachableSessions)

	output := cmd.OutOrStdout()
	if len(listing.Approvals) == 0 {
		_, err := fmt.Fprintln(output, "No pending approvals.")
		return err
	}
	for _, pending := range listing.Approvals {
		if _, err := fmt.Fprintf(
			output,
			"%s\t%s\t%s\t%s\n",
			pending.ApprovalID,
			pending.Action,
			approvalAge(pending.Created),
			pending.Message,
		); err != nil {
			return fmt.Errorf("write approvals output: %w", err)
		}
	}
	return nil
}

func promptApproval(
	cmd *cobra.Command,
	session *client.AgentSession,
	approvalID string,
) error {
	listing, err := session.Approvals(cmd.Context())
	if err != nil {
		return err
	}
	reportUnreachableLaunches(cmd, listing.UnreachableSessions)

	var pending *protocol.PendingApproval
	for index := range listing.Approvals {
		if listing.Approvals[index].ApprovalID == approvalID {
			pending = &listing.Approvals[index]
			break
		}
	}
	if pending == nil {
		return exitcode.New(1, "no pending approval %q", approvalID)
	}

	output := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(
		output,
		"Action:  %s (%s)\nCreated: %s ago\n%s\n\nApprove? [y/N]: ",
		pending.Action,
		pending.Name,
		approvalAge(pending.Created),
		pending.Message,
	); err != nil {
		return fmt.Errorf("write approval description: %w", err)
	}

	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read approval decision: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	approve := answer == "y" || answer == "yes"

	return decideApproval(cmd, session, approvalID, approve)
}

func decideApproval(
	cmd *cobra.Command,
	session *client.AgentSession,
	approvalID string,
	approve bool,
) error {
	decision, err := session.DecideApproval(cmd.Context(), approvalID, approve)
	if err != nil {
		return exitcode.New(1, "decide approval %s: %v", approvalID, err)
	}

	description := decision.Approval.Name
	if decision.Approval.Message != "" {
		description += ": " + decision.Approval.Message
	}
	output := cmd.OutOrStdout()
	switch {
	case decision.Changed && approve:
		_, err = fmt.Fprintf(
			output,
			"Approved %s (%s). The launch runs it now; the waiting agent receives the result.\n",
			approvalID,
			description,
		)
	case decision.Changed:
		_, err = fmt.Fprintf(output, "Denied %s (%s).\n", approvalID, description)
	case approve && decision.Status == "approved":
		_, err = fmt.Fprintf(output, "Approval %s was already approved.\n", approvalID)
	case !approve && decision.Status == "denied":
		_, err = fmt.Fprintf(output, "Approval %s was already denied.\n", approvalID)
	case approve:
		return exitcode.New(
			1,
			"approval %s was already denied; denies are final - re-run the action to create a new approval",
			approvalID,
		)
	default:
		return exitcode.New(
			1,
			"approval %s was already approved and cannot be denied",
			approvalID,
		)
	}
	if err != nil {
		return fmt.Errorf("write approval decision output: %w", err)
	}
	return nil
}

func reportUnreachableLaunches(cmd *cobra.Command, unreachable uint64) {
	if unreachable == 0 {
		return
	}
	fmt.Fprintf(
		cmd.ErrOrStderr(),
		"%d launch session(s) did not answer; their approvals are not shown.\n",
		unreachable,
	)
}

func approvalAge(created time.Time) string {
	age := time.Since(created)
	if age < 0 {
		age = 0
	}
	return age.Truncate(time.Second).String()
}
