package diagnostic

// Centralizes errors that are intentionally excluded from operation results.

// DiscardError preserves the shape of an intentionally discarded diagnostic.
// Reason explains why the error is discarded, while message and attributes
// match the record that would otherwise be logged.
func DiscardError(
	reason string,
	message string,
	err error,
	attributes ...any,
) {
	_ = reason
	_ = message
	_ = err
	_ = attributes
}
