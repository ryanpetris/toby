// Shared daemon errors and the fx root-cause unwrapper (so graph start failures
// surface the underlying error rather than dig's wrapper).

package daemon

import (
	"errors"

	"go.uber.org/dig"
)

// errBadBringUpRequest is returned when the registry hands BringUp a request of an
// unexpected type (a programming error, not a user-facing condition).
var errBadBringUpRequest = errors.New("daemon: invalid bring-up request")

func fxRootCause(err error) error {
	if err == nil {
		return nil
	}
	if cause := dig.RootCause(err); cause != nil {
		var digErr dig.Error
		if !errors.As(cause, &digErr) {
			return cause
		}
	}
	return err
}
