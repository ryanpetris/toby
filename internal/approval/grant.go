package approval

// In-process execution grants. A grant authorizes exactly one re-dispatch of a
// held, approved request. It travels only as a context value with an
// unexported key inside the launch process, so nothing arriving over a wire
// can carry one.

import "context"

type grantKey struct{}

// WithGrant marks ctx as executing the approved record id. Only the
// approve-time executor attaches grants.
func WithGrant(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, grantKey{}, id)
}

func grantFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(grantKey{}).(string)
	return id, ok && id != ""
}
