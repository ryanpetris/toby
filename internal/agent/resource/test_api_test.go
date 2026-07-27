package resource

// Provides package-local lifecycle conveniences and state snapshots for tests.

import (
	"sort"
	"time"
)

type resourceStatus struct {
	Key                 Summary
	Generation          uint64
	State               State
	Leases              uint64
	OpeningLeases       uint64
	Connectors          uint64
	StartedAt           time.Time
	ReadyAt             time.Time
	IdleDeadline        time.Time
	RetryDeadline       time.Time
	UpdatedAt           time.Time
	ConsecutiveFailures uint32
	LastError           string
}

func (r *Registry) status() []resourceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]resourceStatus, 0, len(r.entries))
	for _, current := range r.entries {
		var opening uint64
		for lease := range current.leases {
			if lease.state == LeaseOpening {
				opening++
			}
		}

		result = append(result, resourceStatus{
			Key:                 current.key.Summary(),
			Generation:          current.generation,
			State:               current.state,
			Leases:              uint64(len(current.leases)),
			OpeningLeases:       opening,
			Connectors:          uint64(len(current.connectors)),
			StartedAt:           current.startedAt,
			ReadyAt:             current.readyAt,
			IdleDeadline:        current.idleDeadline,
			RetryDeadline:       current.retryDeadline,
			UpdatedAt:           current.updatedAt,
			ConsecutiveFailures: current.failures,
			LastError:           current.lastError,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Key.ID < result[j].Key.ID
	})

	return result
}
