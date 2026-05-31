package commands

import (
	"fmt"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/diagnostic/exitcode"

	"github.com/spf13/cobra"
)

func newSandboxGitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Run host Git commands for visible repositories.",
	}
	commit := &cobra.Command{
		Use:   "commit REPOSITORY -m MESSAGE [--amend]",
		Short: "Commit staged files in a visible repository using host Git.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, _ := cmd.Flags().GetString("message")
			if message == "" {
				return exitcode.New(2, "commit message is required")
			}
			amend, _ := cmd.Flags().GetBool("amend")
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitCommit(args[0], message, amend)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	commit.Flags().StringP("message", "m", "", "Commit message passed to git commit -m.")
	commit.Flags().Bool("amend", false, "Amend the previous commit.")
	cmd.AddCommand(commit)
	cmd.AddCommand(&cobra.Command{
		Use:   "fetch REPOSITORY",
		Short: "Fetch remote refs in a visible repository using host Git.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitFetch(args[0])
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	})
	push := &cobra.Command{
		Use:   "push REPOSITORY BRANCH [ORIGIN]",
		Short: "Push one branch from a visible repository using host Git.",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			origin := ""
			if len(args) == 3 {
				origin = args[2]
			}
			tags, _ := cmd.Flags().GetBool("tags")
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitPush(args[0], args[1], origin, tags)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	push.Flags().Bool("tags", false, "Push all tags with git push --tags.")
	cmd.AddCommand(push)
	rebase := &cobra.Command{
		Use:   "rebase REPOSITORY BASE|--continue|--abort",
		Short: "Start, continue, or abort a rebase using host Git.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			continueRebase, _ := cmd.Flags().GetBool("continue")
			abort, _ := cmd.Flags().GetBool("abort")
			if continueRebase && abort {
				return exitcode.New(2, "rebase accepts only one of --continue or --abort")
			}
			base := ""
			if continueRebase || abort {
				if len(args) != 1 {
					return exitcode.New(2, "rebase --continue and --abort accept only REPOSITORY")
				}
			} else {
				if len(args) != 2 {
					return exitcode.New(2, "rebase base is required")
				}
				base = args[1]
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitRebase(args[0], base, continueRebase, abort)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	rebase.Flags().Bool("continue", false, "Continue an in-progress rebase.")
	rebase.Flags().Bool("abort", false, "Abort an in-progress rebase.")
	cmd.AddCommand(rebase)
	tag := &cobra.Command{
		Use:   "tag REPOSITORY TAG -m MESSAGE [TARGET]",
		Short: "Create an annotated tag in a visible repository using host Git.",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, _ := cmd.Flags().GetString("message")
			if message == "" {
				return exitcode.New(2, "tag message is required")
			}
			target := ""
			if len(args) == 3 {
				target = args[2]
			}
			client, err := sandboxControlClient()
			if err != nil {
				return err
			}
			result, err := client.GitTag(args[0], args[1], message, target)
			if err != nil {
				return err
			}
			return writeGitResult(cmd, result)
		},
	}
	tag.Flags().StringP("message", "m", "", "Tag message passed to git tag -m.")
	cmd.AddCommand(tag)
	return cmd
}

func sandboxControlClient() (*control.Client, error) {
	endpoint, err := control.DefaultEndpoint()
	if err != nil {
		return nil, err
	}
	return control.NewEndpointClient(endpoint), nil
}

func writeGitResult(cmd *cobra.Command, result control.GitResult) error {
	if result.Stdout != "" {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), result.Stderr)
	}
	if result.ExitCode != 0 {
		return exitcode.Code(result.ExitCode)
	}
	return nil
}
