// Label-scoped lookup and id-based removal, used by the daemon's orphan sweep (remove
// leftover toby.tool.* / unowned home/netns containers at startup) and by the cascade
// that tears a project's tool containers down. These operate by container id via the
// moby client, independent of the in-memory records (orphans are not tracked).

package engine

import (
	"context"

	"github.com/moby/moby/client"
)

// ListByLabel returns the ids of all containers (running or not) carrying label=value.
func (s *Service) ListByLabel(ctx context.Context, label, value string) ([]string, error) {
	cli, err := s.Client(ctx)
	if err != nil {
		return nil, err
	}
	result, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", label+"="+value),
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// RemoveByID force-removes a container by id and drops any tracking record. Used for
// ephemeral tool containers and orphan cleanup, where no testcontainers handle exists.
func (s *Service) RemoveByID(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	delete(s.records, id)
	s.mu.Unlock()

	cli, err := s.Client(ctx)
	if err != nil {
		return err
	}
	_, err = cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}
