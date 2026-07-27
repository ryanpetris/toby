package protocol

// Defines bounded, non-secret error categories.

// ErrorCode identifies a protocol-level failure category.
type ErrorCode string

const (
	// ErrorInvalidRequest reports an invalid protocol request.
	ErrorInvalidRequest ErrorCode = "invalid_request"
	// ErrorAcquireFailed reports a failed resource acquisition.
	ErrorAcquireFailed ErrorCode = "acquire_failed"
	// ErrorLeaseNotFound reports an unknown or expired lease.
	ErrorLeaseNotFound ErrorCode = "lease_not_found"
	// ErrorUnavailable reports a temporarily unavailable resource.
	ErrorUnavailable ErrorCode = "unavailable"
	// ErrorInternal reports an internal agent failure.
	ErrorInternal ErrorCode = "internal"
)
