package prepareclient

// Follows one agent-owned OCI preparation stream and presents its progress.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/status"
)

type eventStream interface {
	Recv() (protocol.OCIEvent, error)
	Close() error
}

// Presentation creates one visible status operation only when preparation
// performs work or fails.
type Presentation struct {
	Start         func() *status.Operation
	CompleteLabel string
	Stdout        io.Writer
	Stderr        io.Writer
	Logger        *diagnostic.Logger
}

type lazyPresentation struct {
	options       Presentation
	operation     *status.Operation
	stdout        io.Writer
	stderr        io.Writer
	outputVisible bool
}

// Follow requests one prepared image and consumes the operation through its
// terminal result.
func Follow(
	ctx context.Context,
	resources *clientresource.Registry,
	clientID protocol.ClientResourceID,
	reference string,
	presentation Presentation,
) (returnErr error) {
	if resources == nil {
		return fmt.Errorf("OCI client resource registry is required")
	}
	stream, err := resources.PrepareOCI(ctx, clientID)
	if err != nil {
		return fmt.Errorf("open OCI image %q stream: %w", reference, err)
	}
	return followStream(reference, stream, presentation)
}

func followStream(
	reference string,
	stream eventStream,
	options Presentation,
) (returnErr error) {
	if stream == nil {
		return fmt.Errorf("OCI event stream is required")
	}
	presentation, err := newLazyPresentation(options)
	if err != nil {
		return err
	}
	defer func() {
		options.Logger.DebugError(
			"close OCI preparation event stream",
			stream.Close(),
			"image", reference,
		)
		returnErr = presentation.finish(returnErr)
	}()

	var operationID protocol.OperationID
	var sequence uint64
	for {
		event, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf(
					"OCI image %q preparation ended without a result",
					reference,
				)
			}
			return fmt.Errorf(
				"read OCI image %q preparation: %w",
				reference,
				err,
			)
		}

		switch event.Kind {
		case protocol.OCIEventAccepted:
			if operationID != "" {
				return fmt.Errorf(
					"OCI image %q preparation was accepted twice",
					reference,
				)
			}
			operationID = event.OperationID
		case protocol.OCIEventSnapshot:
			if operationID == "" {
				return fmt.Errorf(
					"OCI image %q returned a snapshot before acceptance",
					reference,
				)
			}
			if event.OperationID != operationID {
				return fmt.Errorf(
					"OCI image %q returned an unexpected operation ID",
					reference,
				)
			}
			if sequence != 0 {
				return fmt.Errorf(
					"OCI image %q returned more than one initial snapshot",
					reference,
				)
			}
			sequence = event.Sequence
			if event.Progress == nil {
				return fmt.Errorf(
					"OCI image %q returned an empty progress snapshot",
					reference,
				)
			}
			if err := presentation.applyProgress(
				reference,
				*event.Progress,
			); err != nil {
				return err
			}
		case protocol.OCIEventProgress:
			if err := validateEvent(
				reference,
				operationID,
				&sequence,
				event.OperationID,
				event.Sequence,
			); err != nil {
				return err
			}
			if event.Progress == nil {
				return fmt.Errorf(
					"OCI image %q returned an empty progress update",
					reference,
				)
			}
			if err := presentation.applyProgress(
				reference,
				*event.Progress,
			); err != nil {
				return err
			}
		case protocol.OCIEventOutput:
			if err := validateEvent(
				reference,
				operationID,
				&sequence,
				event.OperationID,
				event.Sequence,
			); err != nil {
				return err
			}
			if err := presentation.write(
				event.Stream,
				event.Data,
			); err != nil {
				return fmt.Errorf(
					"present OCI image %q output: %w",
					reference,
					err,
				)
			}
		case protocol.OCIEventComplete:
			if err := validateEvent(
				reference,
				operationID,
				&sequence,
				event.OperationID,
				event.Sequence,
			); err != nil {
				return err
			}
			if !event.Cached {
				if err := presentation.ensure(); err != nil {
					return err
				}
			}
			return nil
		case protocol.OCIEventFailed:
			if err := validateEvent(
				reference,
				operationID,
				&sequence,
				event.OperationID,
				event.Sequence,
			); err != nil {
				return err
			}
			if err := presentation.ensure(); err != nil {
				return err
			}
			return fmt.Errorf(
				"prepare OCI image %q: %s",
				reference,
				event.Message,
			)
		default:
			return fmt.Errorf(
				"OCI image %q preparation returned event %q",
				reference,
				event.Kind,
			)
		}
	}
}

func newLazyPresentation(
	options Presentation,
) (*lazyPresentation, error) {
	if options.Start == nil {
		return nil, fmt.Errorf("OCI status operation factory is required")
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}

	return &lazyPresentation{options: options}, nil
}

func (p *lazyPresentation) ensure() error {
	if p.operation != nil {
		return nil
	}
	p.operation = p.options.Start()
	if p.operation == nil {
		return fmt.Errorf("OCI status operation factory returned nil")
	}
	p.stdout = p.operation.Writer(p.options.Stdout)
	p.stderr = p.operation.Writer(p.options.Stderr)

	return nil
}

func (p *lazyPresentation) applyProgress(
	reference string,
	progress protocol.OCIProgressState,
) error {
	if err := p.ensure(); err != nil {
		return err
	}
	if p.outputVisible {
		p.operation.ClearOutput()
		p.outputVisible = false
	}
	applyProgress(p.operation, reference, progress)

	return nil
}

func (p *lazyPresentation) write(
	stream protocol.OutputStream,
	data []byte,
) error {
	if err := p.ensure(); err != nil {
		return err
	}

	var writer io.Writer
	switch stream {
	case protocol.OutputStdout:
		writer = p.stdout
	case protocol.OutputStderr:
		writer = p.stderr
	default:
		return fmt.Errorf("unknown output stream %q", stream)
	}
	_, err := writer.Write(data)
	if err == nil && len(data) != 0 {
		p.outputVisible = true
	}
	return err
}

func (p *lazyPresentation) finish(operationErr error) error {
	if operationErr != nil && p.operation == nil {
		p.options.Logger.DebugError(
			"present OCI preparation failure",
			p.ensure(),
		)
	}
	if p.operation == nil {
		return operationErr
	}
	if operationErr != nil || p.options.CompleteLabel == "" {
		p.operation.Finish(operationErr)
	} else {
		p.operation.Complete(p.options.CompleteLabel)
	}

	return operationErr
}

func applyProgress(
	operation interface {
		SetLabel(string)
		SetProgress(status.Progress)
	},
	reference string,
	progress protocol.OCIProgressState,
) {
	action := "Preparing"
	label := "Preparing OCI image " + reference
	switch progress.Phase {
	case protocol.OCIProgressResolving:
		action = "Resolving"
		label = "Resolving OCI image " + reference
	case protocol.OCIProgressDownloading:
		action = "Pulling"
		label = "Pulling OCI image " + reference
	case protocol.OCIProgressExtracting:
		action = "Extracting"
		label = "Extracting OCI image " + reference
	}

	operation.SetLabel(label)
	operation.SetProgress(status.Progress{
		CompletedBytes: progress.CompletedBytes,
		TotalBytes:     progress.TotalBytes,
		CompletedItems: progress.CompletedItems,
		TotalItems:     progress.TotalItems,
		OCIAction:      action,
		OCIReference:   reference,
	})
}

func validateEvent(
	reference string,
	accepted protocol.OperationID,
	sequence *uint64,
	actual protocol.OperationID,
	actualSequence uint64,
) error {
	if accepted == "" {
		return fmt.Errorf(
			"OCI image %q returned output before acceptance",
			reference,
		)
	}
	if actual != accepted {
		return fmt.Errorf(
			"OCI image %q returned an unexpected operation ID",
			reference,
		)
	}
	if actualSequence != *sequence+1 {
		return fmt.Errorf(
			"OCI image %q returned operation sequence %d after %d",
			reference,
			actualSequence,
			*sequence,
		)
	}
	*sequence = actualSequence

	return nil
}
