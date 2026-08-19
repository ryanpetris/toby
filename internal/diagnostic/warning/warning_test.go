package warning

// Tests warning registration, formatting, and suppression.

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSuppressionFromList(t *testing.T) {
	all, unknown := SuppressionFromList([]string{"*"})
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v", unknown)
	}
	if !all.Set || !all.All || !all.Suppresses(ProjectDuplicate) {
		t.Fatalf("all suppression = %#v", all)
	}

	ids, unknown := SuppressionFromList(
		[]string{" project.duplicate ", "project.missing"},
	)
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v", unknown)
	}
	if !ids.Set || ids.All || !ids.Suppresses(ProjectDuplicate) ||
		!ids.Suppresses(ProjectMissing) ||
		ids.Suppresses(PermissionAutoDeny) {
		t.Fatalf("id suppression = %#v", ids)
	}

	empty, unknown := SuppressionFromList(nil)
	if len(unknown) != 0 {
		t.Fatalf("unknown = %#v", unknown)
	}
	if !empty.Set || empty.All || empty.Suppresses(ProjectDuplicate) {
		t.Fatalf("empty suppression = %#v", empty)
	}
}

func TestSuppressionFromListSkipsUnknownIDs(t *testing.T) {
	suppression, unknown := SuppressionFromList(
		[]string{"unknown.warning", "project.missing"},
	)
	if !reflect.DeepEqual(unknown, []string{"unknown.warning"}) {
		t.Fatalf("unknown = %#v", unknown)
	}
	if !suppression.Suppresses(ProjectMissing) ||
		suppression.Suppresses(PermissionAutoDeny) {
		t.Fatalf("suppression = %#v", suppression)
	}
}

func TestSuppressionCloneAndMerge(t *testing.T) {
	src := Suppression{Set: true, IDs: map[ID]bool{ProjectDuplicate: true}}
	clone := src.Clone()
	clone.IDs[ProjectDuplicate] = false
	if !src.Suppresses(ProjectDuplicate) {
		t.Fatalf("source changed after clone mutation: %#v", src)
	}

	dst := Suppression{Set: true, IDs: map[ID]bool{ProjectMissing: true}}
	dst.Merge(Suppression{})
	if !dst.Suppresses(ProjectMissing) {
		t.Fatalf("unset merge changed suppression: %#v", dst)
	}
	dst.Merge(src)
	src.IDs[ProjectDuplicate] = false
	// Merge unions src into dst and copies src's IDs.
	if !dst.Suppresses(ProjectMissing) || !dst.Suppresses(ProjectDuplicate) {
		t.Fatalf("merge should union and copy source ids: %#v", dst)
	}

	// Merging an All suppression unions the All flag.
	allDst := Suppression{Set: true, IDs: map[ID]bool{ProjectMissing: true}}
	allDst.Merge(Suppression{Set: true, All: true})
	if !allDst.All || !allDst.Suppresses(ProjectMissing) {
		t.Fatalf("merge should union All: %#v", allDst)
	}
}

func TestServiceUsesCurrentSuppression(t *testing.T) {
	logger := &recordingLogger{}
	suppression := Suppression{}
	service := NewService(logger, func() Suppression {
		return suppression
	})

	service.Warn(ProjectMissing, "first")
	suppression = Suppression{
		IDs: map[ID]bool{
			ProjectMissing: true,
		},
	}
	service.Warn(ProjectMissing, "second")

	if len(logger.messages) != 1 {
		t.Fatalf("messages = %#v, want one warning", logger.messages)
	}
	if got, want := logger.messages[0],
		"warning[project.missing]: first"; got != want {
		t.Fatalf("warning message = %q, want %q", got, want)
	}
	if got, want := logger.attributes[0],
		[]any{"warning_id", "project.missing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning attributes = %#v, want %#v", got, want)
	}
}

func TestServiceForwardsStructuredWarningError(t *testing.T) {
	logger := &recordingLogger{}
	service := NewService(logger, nil)
	sentinel := errors.New("broken")

	service.WarnError(
		ProjectMissing,
		"project lookup failed",
		sentinel,
		"project_name", "demo",
	)

	if got, want := logger.messages,
		[]string{"warning[project.missing]: project lookup failed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning messages = %#v, want %#v", got, want)
	}
	if got, want := logger.errors, []error{sentinel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning errors = %#v, want %#v", got, want)
	}
	if got, want := logger.attributes[0],
		[]any{
			"warning_id", "project.missing",
			"project_name", "demo",
		}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning attributes = %#v, want %#v", got, want)
	}
}

func TestServiceRejectsUnregisteredID(t *testing.T) {
	logger := &recordingLogger{}
	service := NewService(logger, nil)

	service.Warn(ID("unknown.warning"), "not emitted")

	if len(logger.messages) != 0 {
		t.Fatalf("unregistered warning emitted %#v", logger.messages)
	}
	if got, want := logger.errorMessages,
		[]string{"cannot emit unregistered warning[unknown.warning]"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("error messages = %#v, want %#v", got, want)
	}
	if got, want := logger.errorAttributes[0],
		[]any{"warning_id", "unknown.warning"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("error attributes = %#v, want %#v", got, want)
	}
}

type recordingLogger struct {
	messages        []string
	attributes      [][]any
	errors          []error
	errorMessages   []string
	errorAttributes [][]any
}

func (l *recordingLogger) Warn(message string, attributes ...any) {
	l.messages = append(l.messages, message)
	l.attributes = append(l.attributes, attributes)
}

func (l *recordingLogger) WarnError(
	message string,
	err error,
	attributes ...any,
) {
	l.Warn(message, attributes...)
	l.errors = append(l.errors, err)
}

func (l *recordingLogger) Error(message string, attributes ...any) {
	l.errorMessages = append(l.errorMessages, message)
	l.errorAttributes = append(l.errorAttributes, attributes)
}

func TestParseIDTrimsAndRejectsUnknownIDs(t *testing.T) {
	if id, err := ParseID(" project.autoload-disabled "); err != nil || id != ProjectAutoloadDisabled {
		t.Fatalf("ParseID = %q, %v", id, err)
	}
	if _, err := ParseID("unknown"); err == nil ||
		!strings.Contains(err.Error(), `warning id "unknown" is not registered`) {
		t.Fatalf("ParseID unknown = %v", err)
	}
}
