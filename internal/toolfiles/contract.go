package toolfiles

// Defines the tool contribution contract and run identity applied to generated
// native files.

// Ownership is the sandbox user's host identity for one launch.
type Ownership struct {
	UID int
	GID int
}

// Contributor renders the complete set of native files owned by one tool.
type Contributor interface {
	// ToolFiles returns the generated files contributed by the tool.
	ToolFiles(Ownership) ([]File, error)
}
