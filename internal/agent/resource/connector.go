package resource

// Tracks generation-bound client connectors and invalidates them on process
// failure or agent shutdown.

// Connector is one active client connection to a resource generation.
type Connector struct {
	registry   *Registry
	key        Key
	generation uint64
	done       chan struct{}
	err        error
	closed     bool
}

// OpenConnector reserves an active connector on the lease's generation.
func (l *Lease) OpenConnector() (*Connector, error) {
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closing {
		return nil, ErrShuttingDown
	}
	if l.state != LeaseActive {
		return nil, ErrLeaseClosed
	}

	current := r.entries[l.key]
	if current == nil || current.generation != l.generation || current.state != StateReady || current.instance == nil {
		return nil, ErrResourceUnavailable
	}

	connector := &Connector{
		registry:   r,
		key:        l.key,
		generation: l.generation,
		done:       make(chan struct{}),
	}
	current.connectors[connector] = struct{}{}
	current.updatedAt = r.options.Clock.Now()

	return connector, nil
}

// Done closes when the connector is closed or its generation is invalidated.
func (c *Connector) Done() <-chan struct{} {
	return c.done
}

// Err reports why the connector closed. A normal Close records no error.
func (c *Connector) Err() error {
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()

	return c.err
}

// Close idempotently releases this connector.
func (c *Connector) Close() {
	r := c.registry
	r.mu.Lock()
	defer r.mu.Unlock()

	if c.closed {
		return
	}

	current := r.entries[c.key]
	if current != nil {
		delete(current.connectors, c)
	}
	r.closeConnectorLocked(c, nil)

	if current != nil {
		r.maybeIdleLocked(current)
	}
}

func (r *Registry) closeConnectorLocked(connector *Connector, cause error) {
	if connector.closed {
		return
	}

	connector.closed = true
	connector.err = cause
	close(connector.done)
}

func (r *Registry) invalidateConnectorsLocked(current *entry, cause error) {
	for connector := range current.connectors {
		delete(current.connectors, connector)
		r.closeConnectorLocked(connector, cause)
	}
}
