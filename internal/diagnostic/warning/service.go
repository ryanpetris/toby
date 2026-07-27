package warning

// Process-wide warning emission with launch-sensitive suppression.

import "fmt"

// Logger is the diagnostic surface consumed by warning emission.
type Logger interface {
	Warn(message string, args ...any)
	WarnError(message string, err error, args ...any)
	Error(message string, args ...any)
}

// Service emits registered warnings using the currently effective suppression
// configuration.
type Service struct {
	logger      Logger
	suppression func() Suppression
}

// NewService constructs a warning service. A nil suppression source means no
// warnings are suppressed.
func NewService(
	logger Logger,
	suppression func() Suppression,
) *Service {
	return &Service{
		logger:      logger,
		suppression: suppression,
	}
}

// Warn emits one suppressible structured warning.
func (s *Service) Warn(id ID, message string, attributes ...any) {
	if !s.shouldEmit(id) {
		return
	}

	s.logger.Warn(
		warningMessage(id, message),
		warningAttributes(id, attributes)...,
	)
}

// WarnError emits one suppressible structured warning retaining err in both
// human-readable and structured output.
func (s *Service) WarnError(
	id ID,
	message string,
	err error,
	attributes ...any,
) {
	if err == nil || !s.shouldEmit(id) {
		return
	}

	s.logger.WarnError(
		warningMessage(id, message),
		err,
		warningAttributes(id, attributes)...,
	)
}

func (s *Service) shouldEmit(id ID) bool {
	if s == nil || s.logger == nil {
		return false
	}
	if _, err := ParseID(string(id)); err != nil {
		s.logger.Error(
			fmt.Sprintf("cannot emit unregistered warning[%s]", id),
			"warning_id", string(id),
		)
		return false
	}

	var suppression Suppression
	if s.suppression != nil {
		suppression = s.suppression()
	}
	return !suppression.Suppresses(id)
}

func warningMessage(id ID, message string) string {
	return fmt.Sprintf("warning[%s]: %s", id, message)
}

func warningAttributes(id ID, attributes []any) []any {
	result := make([]any, 0, len(attributes)+2)
	result = append(result, "warning_id", string(id))
	return append(result, attributes...)
}
