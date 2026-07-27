package httpbridge

// Confirms each downstream message has been admitted for HTTP dispatch.

import (
	"context"
	"sync"
)

type dispatchReceiptKey struct{}

type dispatchReceipt struct {
	once    sync.Once
	entered chan struct{}
}

func newDispatchReceipt() *dispatchReceipt {
	return &dispatchReceipt{
		entered: make(chan struct{}),
	}
}

func withDispatchReceipt(
	ctx context.Context,
	receipt *dispatchReceipt,
) context.Context {
	return context.WithValue(ctx, dispatchReceiptKey{}, receipt)
}

func signalRequestDispatched(ctx context.Context) {
	receipt, _ := ctx.Value(dispatchReceiptKey{}).(*dispatchReceipt)
	if receipt != nil {
		receipt.signal()
	}
}

func (r *dispatchReceipt) signal() {
	r.once.Do(func() {
		close(r.entered)
	})
}

func (r *dispatchReceipt) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-r.entered:
		return nil
	}
}
