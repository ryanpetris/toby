package cli

// Builds per-user agent lifecycle, resource, log, models, and cache commands.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/protocol"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/version"

	"github.com/spf13/cobra"
)

const (
	agentCommandName           = "agent"
	agentStatusCommandName     = "status"
	agentStopCommandName       = "stop"
	agentResourcesCommandName  = "resources"
	agentLogsCommandName       = "logs"
	agentModelsCommandName     = "models"
	agentCacheCommandName      = "cache"
	agentCacheFlushCommandName = "flush"
)

func isConfigFreeAgentInvocation(arguments []string) bool {
	for _, name := range [...]string{
		agentStatusCommandName,
		agentStopCommandName,
		agentResourcesCommandName,
		agentLogsCommandName,
	} {
		if isAgentSubcommandInvocation(arguments, name) {
			return true
		}
	}
	return false
}

func isAgentSubcommandInvocation(
	arguments []string,
	subcommandName string,
) bool {
	flags := rootFlagValues{
		managedTerminal: true,
	}
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

	agentCommand := &cobra.Command{
		Use:  agentCommandName,
		Args: cobra.NoArgs,
	}
	subcommand := &cobra.Command{
		Use:  subcommandName,
		Args: cobra.NoArgs,
		Run:  func(*cobra.Command, []string) {},
	}
	agentCommand.AddCommand(subcommand)
	root.AddCommand(agentCommand)

	executed, err := root.ExecuteC()
	if err != nil && executed == nil {
		return false
	}
	return executed == subcommand
}

func newAgentCommand(params Params) *cobra.Command {
	command := &cobra.Command{
		Use:   agentCommandName,
		Short: "Manage the per-user Toby agent.",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newAgentStatusCommand(params),
		newAgentStopCommand(params),
		newAgentResourcesCommand(params),
		newAgentLogsCommand(params),
		newAgentModelsCommand(params),
		newAgentCacheCommand(params),
	)

	return command
}

func newAgentModelsCommand(params Params) *cobra.Command {
	return &cobra.Command{
		Use:   agentModelsCommandName + " <configured-name-or-resource-id>",
		Short: "List models for one configured agent resource.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, lease, err := acquireAgentModelsResource(
				cmd.Context(),
				params,
				args[0],
			)
			if err != nil {
				return err
			}

			items, listErr := session.ListModels(
				cmd.Context(),
				lease,
			)
			logger := params.Diagnostics.Logger("cli.agent")
			logger.DebugError(
				"release models lease after listing",
				lease.Release(cmd.Context()),
			)
			logger.DebugError(
				"close agent session after listing models",
				session.Close(),
			)
			if listErr != nil {
				return listErr
			}

			output := cmd.OutOrStdout()
			for _, item := range items {
				if _, err := fmt.Fprintf(
					output,
					"%s\t%s\n",
					item.ModelID,
					item.Model,
				); err != nil {
					return fmt.Errorf("write agent models output: %w", err)
				}
			}

			return nil
		},
	}
}

func newAgentCacheCommand(params Params) *cobra.Command {
	command := &cobra.Command{
		Use:   agentCacheCommandName,
		Short: "Manage agent resource caches.",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use: agentCacheFlushCommandName +
			" <configured-name-or-resource-id>",
		Short: "Flush one models resource cache.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, lease, err := acquireAgentModelsResource(
				cmd.Context(),
				params,
				args[0],
			)
			if err != nil {
				return err
			}

			flushErr := session.FlushModelsCache(
				cmd.Context(),
				lease,
			)
			logger := params.Diagnostics.Logger("cli.agent")
			logger.DebugError(
				"release models lease after cache flush",
				lease.Release(cmd.Context()),
			)
			logger.DebugError(
				"close agent session after cache flush",
				session.Close(),
			)

			return flushErr
		},
	})

	return command
}

func acquireAgentModelsResource(
	ctx context.Context,
	params Params,
	selector string,
) (
	session *agentclient.AgentSession,
	result *agentclient.ResourceLease,
	returnErr error,
) {
	if params.Agent == nil {
		return nil, nil, fmt.Errorf("agent client is not configured")
	}
	if params.TobyConfig == nil {
		return nil, nil, fmt.Errorf("configuration is unavailable")
	}

	session, err := params.Agent.OpenAgent(ctx, nil)
	if err != nil {
		return nil, nil, exitcode.New(
			1,
			"agent is not running or unavailable: %v",
			err,
		)
	}
	defer func() {
		if returnErr != nil {
			params.Diagnostics.Logger("cli.agent").DebugError(
				"close agent session after resource acquisition failure",
				session.Close(),
			)
		}
	}()

	resources, err := params.TobyConfig.ResolveResources(params.Warnings)
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(resources.Models))
	for name := range resources.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	if _, configured := resources.Models[selector]; configured {
		names = []string{selector}
	}

	for _, name := range names {
		effective, err := resolveAgentModelsConfiguration(
			params,
			name,
			resources.Models[name],
		)
		if err != nil {
			return nil, nil, err
		}
		raw, err := json.Marshal(effective)
		if err != nil {
			return nil, nil, err
		}
		lease, err := session.Acquire(
			ctx,
			protocol.ResourceModels,
			raw,
		)
		clear(raw)
		if err != nil {
			return nil, nil, err
		}
		if name == selector ||
			string(lease.ResourceID()) == selector {
			return session, lease, nil
		}
		if err := lease.Release(ctx); err != nil {
			return nil, nil, err
		}
	}

	return nil, nil, fmt.Errorf(
		"models resource %q is not configured",
		selector,
	)
}

func resolveAgentModelsConfiguration(
	params Params,
	name string,
	model modelsconfig.Config,
) (modelsconfig.Config, error) {
	headers, err := params.TobyConfig.ResolveModelHeaders(name, model)
	if err != nil {
		return modelsconfig.Config{}, err
	}

	effective := model.Clone()
	effective.Headers = make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) != 1 {
			return modelsconfig.Config{}, fmt.Errorf(
				"models resource header %q has %d values",
				name,
				len(values),
			)
		}
		effective.Headers[name] = values[0]
	}
	if len(effective.Headers) == 0 {
		effective.Headers = nil
	}

	return modelsconfig.Normalize(effective)
}

func newAgentStopCommand(params Params) *cobra.Command {
	return &cobra.Command{
		Use:   agentStopCommandName,
		Short: "Stop the agent and its background resources.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if params.Agent == nil {
				return fmt.Errorf("agent client is not configured")
			}

			if err := params.Agent.Stop(cmd.Context()); err != nil {
				return exitcode.New(
					1,
					"stop agent: %v",
					err,
				)
			}

			if _, err := fmt.Fprintln(
				cmd.OutOrStdout(),
				"Toby agent stopped.",
			); err != nil {
				return fmt.Errorf("write agent stop result: %w", err)
			}
			return nil
		},
	}
}

func newAgentStatusCommand(params Params) *cobra.Command {
	return &cobra.Command{
		Use:   agentStatusCommandName,
		Short: "Show non-secret agent status.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if params.Agent == nil {
				return fmt.Errorf("agent client is not configured")
			}

			status, err := params.Agent.Status(cmd.Context())
			if err != nil {
				return exitcode.New(
					1,
					"agent is not running or unavailable: %v",
					err,
				)
			}

			output := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(
				output,
				"state: %s\nversion: %s\nactive sessions: %d\nactive leases: %d\nactive resources: %d\nactive streams: %d\n",
				status.State,
				status.BinaryVersion,
				status.ActiveSessions,
				status.ActiveLeases,
				status.ActiveResources,
				status.ActiveStreams,
			); err != nil {
				return fmt.Errorf("write agent status: %w", err)
			}
			return nil
		},
	}
}

func newAgentResourcesCommand(params Params) *cobra.Command {
	return &cobra.Command{
		Use:   agentResourcesCommandName,
		Short: "List active agent resources without configuration.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if params.Agent == nil {
				return fmt.Errorf("agent client is not configured")
			}

			session, err := params.Agent.OpenAgent(
				cmd.Context(),
				nil,
			)
			if err != nil {
				return exitcode.New(
					1,
					"agent is not running or unavailable: %v",
					err,
				)
			}
			items, listErr := session.Resources(cmd.Context())
			params.Diagnostics.Logger("cli.agent").DebugError(
				"close agent session after resource listing",
				session.Close(),
			)
			if listErr != nil {
				return listErr
			}

			output := cmd.OutOrStdout()
			for _, item := range items {
				if _, err := fmt.Fprintf(
					output,
					"%s\t%s\tleases=%d\n",
					item.Kind,
					item.ResourceID,
					item.ActiveLeases,
				); err != nil {
					return fmt.Errorf(
						"write agent resources output: %w",
						err,
					)
				}
			}

			return nil
		},
	}
}

func newAgentLogsCommand(params Params) *cobra.Command {
	var operation string
	command := &cobra.Command{
		Use:   agentLogsCommandName + " <resource-id>",
		Short: "Print one requested or latest retained agent resource log.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if params.Agent == nil {
				return fmt.Errorf("agent client is not configured")
			}

			session, err := params.Agent.OpenAgent(
				cmd.Context(),
				nil,
			)
			if err != nil {
				return exitcode.New(
					1,
					"agent is not running or unavailable: %v",
					err,
				)
			}

			resourceID := protocol.ResourceID(args[0])
			operationID := protocol.OperationID(operation)
			var output bytes.Buffer
			var readErr error
			for _, kind := range []protocol.ResourceKind{
				protocol.ResourceOCI,
				protocol.ResourceMCP,
				protocol.ResourceModels,
			} {
				output.Reset()
				_, readErr = session.ReadResourceLog(
					cmd.Context(),
					kind,
					resourceID,
					operationID,
					&output,
				)
				if readErr == nil {
					break
				}
			}
			params.Diagnostics.Logger("cli.agent").DebugError(
				"close agent session after resource log read",
				session.Close(),
			)
			if readErr != nil {
				return readErr
			}
			_, err = io.Copy(cmd.OutOrStdout(), &output)
			return err
		},
	}
	command.Flags().StringVar(
		&operation,
		"operation",
		"",
		"read one exact operation or generation ID",
	)

	return command
}
