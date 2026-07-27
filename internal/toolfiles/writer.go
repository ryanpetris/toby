package toolfiles

// Coordinates complete native tool-file validation, preflight, and atomic
// publication for one launch.

import (
	"fmt"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
)

// Writer atomically publishes a validated launch's native tool files.
type Writer struct {
	logger *diagnostic.Logger
}

// NewWriter creates a native tool-file writer.
func NewWriter(logger *diagnostic.Logger) *Writer {
	return &Writer{logger: logger}
}

// Write validates the complete file set and every selected native backing
// before creating a parent or replacing a file. The input plan must not already
// contain generated files. The returned plan records are detached and ordered
// by sandbox target.
func (w *Writer) Write(
	plan bwrap.Plan,
	sources bwrap.Sources,
	files []File,
) (result []bwrap.GeneratedFile, returnErr error) {
	if w == nil {
		return nil, fmt.Errorf("write native tool files: writer is nil")
	}
	if len(plan.GeneratedFiles) != 0 {
		return nil, fmt.Errorf(
			"write native tool files: plan already contains %d generated files",
			len(plan.GeneratedFiles),
		)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("write native tool files: validate base plan: %w", err)
	}

	ordered := cloneFiles(files)
	sortFiles(ordered)
	if err := validateFiles(ordered, plan.Identity); err != nil {
		return nil, fmt.Errorf("write native tool files: %w", err)
	}

	resolved, generated, err := resolveFiles(plan, ordered)
	if err != nil {
		return nil, fmt.Errorf("write native tool files: %w", err)
	}
	candidate := plan.Clone()
	candidate.GeneratedFiles = generated
	if err := candidate.Validate(); err != nil {
		return nil, fmt.Errorf("write native tool files: validate generated-file plan: %w", err)
	}

	backings, err := openBackings(plan, sources, w.logger)
	if err != nil {
		return nil, fmt.Errorf("write native tool files: %w", err)
	}
	defer func() {
		w.logger.DebugError(
			"close generated-file backing directories",
			closeBackings(backings),
		)
	}()

	for index := range resolved {
		resolved[index].directory = backings[resolved[index].backing]
		if err := preflightFile(
			resolved[index],
			w.logger,
		); err != nil {
			return nil, fmt.Errorf(
				"write native tool files: preflight %q owned by %q: %w",
				resolved[index].file.Target,
				resolved[index].file.Owner,
				err,
			)
		}
	}

	for _, file := range resolved {
		if err := writeFile(file, w.logger); err != nil {
			return nil, fmt.Errorf(
				"write native tool files: replace %q owned by %q: %w",
				file.file.Target,
				file.file.Owner,
				err,
			)
		}
	}

	return cloneGeneratedFiles(generated), nil
}
