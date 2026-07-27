package protocol

// Converts internal values to and from the generated agent API.

import (
	"fmt"

	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// ResourceKindToAgent converts one validated resource kind.
func ResourceKindToAgent(kind ResourceKind) agentv1.ResourceKind {
	switch kind {
	case ResourceOCI:
		return agentv1.ResourceKind_RESOURCE_KIND_OCI
	case ResourceMCP:
		return agentv1.ResourceKind_RESOURCE_KIND_MCP
	case ResourceModels:
		return agentv1.ResourceKind_RESOURCE_KIND_MODELS
	default:
		return agentv1.ResourceKind_RESOURCE_KIND_UNSPECIFIED
	}
}

// ResourceKindFromAgent validates and converts one agent API resource kind.
func ResourceKindFromAgent(
	kind agentv1.ResourceKind,
) (ResourceKind, error) {
	switch kind {
	case agentv1.ResourceKind_RESOURCE_KIND_OCI:
		return ResourceOCI, nil
	case agentv1.ResourceKind_RESOURCE_KIND_MCP:
		return ResourceMCP, nil
	case agentv1.ResourceKind_RESOURCE_KIND_MODELS:
		return ResourceModels, nil
	default:
		return "", fmt.Errorf("unknown resource kind %q", kind)
	}
}

// ServiceStateToAgent converts one agent state.
func ServiceStateToAgent(state ServiceState) agentv1.ServiceState {
	switch state {
	case ServiceStarting:
		return agentv1.ServiceState_SERVICE_STATE_STARTING
	case ServiceReady:
		return agentv1.ServiceState_SERVICE_STATE_READY
	case ServiceStopping:
		return agentv1.ServiceState_SERVICE_STATE_STOPPING
	default:
		return agentv1.ServiceState_SERVICE_STATE_UNSPECIFIED
	}
}

// ServiceStateFromAgent validates and converts one agent API service state.
func ServiceStateFromAgent(
	state agentv1.ServiceState,
) (ServiceState, error) {
	switch state {
	case agentv1.ServiceState_SERVICE_STATE_STARTING:
		return ServiceStarting, nil
	case agentv1.ServiceState_SERVICE_STATE_READY:
		return ServiceReady, nil
	case agentv1.ServiceState_SERVICE_STATE_STOPPING:
		return ServiceStopping, nil
	default:
		return "", fmt.Errorf("unknown agent state %q", state)
	}
}

// TransportCapabilityToAgent converts one advertised transport.
func TransportCapabilityToAgent(
	capability TransportCapability,
) agentv1.TransportCapability {
	switch capability {
	case TransportUnixSocket:
		return agentv1.TransportCapability_TRANSPORT_CAPABILITY_UNIX_SOCKET
	default:
		return agentv1.TransportCapability_TRANSPORT_CAPABILITY_UNSPECIFIED
	}
}

// TransportCapabilityFromAgent validates and converts one transport.
func TransportCapabilityFromAgent(
	capability agentv1.TransportCapability,
) (TransportCapability, error) {
	switch capability {
	case agentv1.TransportCapability_TRANSPORT_CAPABILITY_UNIX_SOCKET:
		return TransportUnixSocket, nil
	default:
		return "", fmt.Errorf(
			"unknown transport capability %q",
			capability,
		)
	}
}

// ErrorCodeToAgent converts one bounded agent error code.
func ErrorCodeToAgent(code ErrorCode) agentv1.ErrorCode {
	switch code {
	case ErrorInvalidRequest:
		return agentv1.ErrorCode_ERROR_CODE_INVALID_REQUEST
	case ErrorAcquireFailed:
		return agentv1.ErrorCode_ERROR_CODE_ACQUIRE_FAILED
	case ErrorLeaseNotFound:
		return agentv1.ErrorCode_ERROR_CODE_LEASE_NOT_FOUND
	case ErrorUnavailable:
		return agentv1.ErrorCode_ERROR_CODE_UNAVAILABLE
	case ErrorInternal:
		return agentv1.ErrorCode_ERROR_CODE_INTERNAL
	default:
		return agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED
	}
}

// ErrorCodeFromAgent validates and converts one agent error code.
func ErrorCodeFromAgent(code agentv1.ErrorCode) (ErrorCode, error) {
	switch code {
	case agentv1.ErrorCode_ERROR_CODE_INVALID_REQUEST:
		return ErrorInvalidRequest, nil
	case agentv1.ErrorCode_ERROR_CODE_ACQUIRE_FAILED:
		return ErrorAcquireFailed, nil
	case agentv1.ErrorCode_ERROR_CODE_LEASE_NOT_FOUND:
		return ErrorLeaseNotFound, nil
	case agentv1.ErrorCode_ERROR_CODE_UNAVAILABLE:
		return ErrorUnavailable, nil
	case agentv1.ErrorCode_ERROR_CODE_INTERNAL:
		return ErrorInternal, nil
	default:
		return "", fmt.Errorf("unknown agent error code %q", code)
	}
}

// OutputStreamToAgent converts one subprocess output stream.
func OutputStreamToAgent(stream OutputStream) agentv1.OutputStream {
	switch stream {
	case OutputStdout:
		return agentv1.OutputStream_OUTPUT_STREAM_STDOUT
	case OutputStderr:
		return agentv1.OutputStream_OUTPUT_STREAM_STDERR
	default:
		return agentv1.OutputStream_OUTPUT_STREAM_UNSPECIFIED
	}
}

// OutputStreamFromAgent validates and converts one output stream.
func OutputStreamFromAgent(
	stream agentv1.OutputStream,
) (OutputStream, error) {
	switch stream {
	case agentv1.OutputStream_OUTPUT_STREAM_STDOUT:
		return OutputStdout, nil
	case agentv1.OutputStream_OUTPUT_STREAM_STDERR:
		return OutputStderr, nil
	default:
		return "", fmt.Errorf("unknown output stream %q", stream)
	}
}

// OCISourceToAgent converts one OCI output source.
func OCISourceToAgent(source OCISource) agentv1.OCISource {
	switch source {
	case OCISourceRegistry:
		return agentv1.OCISource_OCI_SOURCE_REGISTRY
	case OCISourceExtractor:
		return agentv1.OCISource_OCI_SOURCE_EXTRACTOR
	case OCISourceCache:
		return agentv1.OCISource_OCI_SOURCE_CACHE
	default:
		return agentv1.OCISource_OCI_SOURCE_UNSPECIFIED
	}
}

// OCISourceFromAgent validates and converts one OCI output source.
func OCISourceFromAgent(source agentv1.OCISource) (OCISource, error) {
	switch source {
	case agentv1.OCISource_OCI_SOURCE_REGISTRY:
		return OCISourceRegistry, nil
	case agentv1.OCISource_OCI_SOURCE_EXTRACTOR:
		return OCISourceExtractor, nil
	case agentv1.OCISource_OCI_SOURCE_CACHE:
		return OCISourceCache, nil
	default:
		return "", fmt.Errorf("unknown OCI output source %q", source)
	}
}

// OCIProgressToAgent converts one complete OCI progress snapshot.
func OCIProgressToAgent(progress OCIProgressState) *agentv1.OCIProgress {
	var phase agentv1.OCIProgressPhase
	switch progress.Phase {
	case OCIProgressResolving:
		phase = agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_RESOLVING
	case OCIProgressDownloading:
		phase = agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_DOWNLOADING
	case OCIProgressExtracting:
		phase = agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_EXTRACTING
	default:
		phase = agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_UNSPECIFIED
	}

	return &agentv1.OCIProgress{
		Phase:          phase,
		CompletedBytes: progress.CompletedBytes,
		TotalBytes:     progress.TotalBytes,
		CompletedItems: progress.CompletedItems,
		TotalItems:     progress.TotalItems,
	}
}

// OCIProgressFromAgent validates and converts one progress snapshot.
func OCIProgressFromAgent(
	progress *agentv1.OCIProgress,
) (OCIProgressState, error) {
	if progress == nil {
		return OCIProgressState{}, fmt.Errorf("OCI progress is empty")
	}

	var phase OCIProgressPhase
	switch progress.GetPhase() {
	case agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_RESOLVING:
		phase = OCIProgressResolving
	case agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_DOWNLOADING:
		phase = OCIProgressDownloading
	case agentv1.OCIProgressPhase_OCI_PROGRESS_PHASE_EXTRACTING:
		phase = OCIProgressExtracting
	default:
		return OCIProgressState{}, fmt.Errorf(
			"unknown OCI progress phase %q",
			progress.GetPhase(),
		)
	}

	result := OCIProgressState{
		Phase:          phase,
		CompletedBytes: progress.GetCompletedBytes(),
		TotalBytes:     progress.GetTotalBytes(),
		CompletedItems: progress.GetCompletedItems(),
		TotalItems:     progress.GetTotalItems(),
	}
	if err := result.validate(); err != nil {
		return OCIProgressState{}, err
	}

	return result, nil
}
