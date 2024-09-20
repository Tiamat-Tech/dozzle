package docker_support

import (
	"context"
	"fmt"
	"time"

	"github.com/amir20/dozzle/internal/docker"
	"github.com/rs/zerolog/log"
)

type ContainerFilter = func(*docker.Container) bool

type HostUnavailableError struct {
	Host docker.Host
	Err  error
}

func (h *HostUnavailableError) Error() string {
	return fmt.Sprintf("host %s unavailable: %v", h.Host.ID, h.Err)
}

type ClientManager interface {
	Find(id string) (ClientService, bool)
	List() []ClientService
	RetryAndList() ([]ClientService, []error)
	Subscribe(ctx context.Context, channel chan<- docker.Host)
	Hosts(ctx context.Context) []docker.Host
}

type MultiHostService struct {
	manager ClientManager
	timeout time.Duration
}

func NewMultiHostService(manager ClientManager, timeout time.Duration) *MultiHostService {
	m := &MultiHostService{
		manager: manager,
		timeout: timeout,
	}

	return m
}

func (m *MultiHostService) FindContainer(host string, id string) (*containerService, error) {
	client, ok := m.manager.Find(host)
	if !ok {
		return nil, fmt.Errorf("host %s not found", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	container, err := client.FindContainer(ctx, id)
	if err != nil {
		return nil, err
	}

	return &containerService{
		clientService: client,
		Container:     container,
	}, nil
}

func (m *MultiHostService) ListContainersForHost(host string) ([]docker.Container, error) {
	client, ok := m.manager.Find(host)
	if !ok {
		return nil, fmt.Errorf("host %s not found", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	return client.ListContainers(ctx)
}

func (m *MultiHostService) ListAllContainers() ([]docker.Container, []error) {
	containers := make([]docker.Container, 0)
	clients, errors := m.manager.RetryAndList()

	for _, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		defer cancel()
		list, err := client.ListContainers(ctx)
		if err != nil {
			host, _ := client.Host(ctx)
			log.Debug().Err(err).Str("host", host.Name).Msg("error listing containers")
			host.Available = false
			errors = append(errors, &HostUnavailableError{Host: host, Err: err})
			continue
		}

		containers = append(containers, list...)
	}

	return containers, errors
}

func (m *MultiHostService) ListAllContainersFiltered(filter ContainerFilter) ([]docker.Container, []error) {
	containers, err := m.ListAllContainers()
	filtered := make([]docker.Container, 0, len(containers))
	for _, container := range containers {
		if filter(&container) {
			filtered = append(filtered, container)
		}
	}
	return filtered, err
}

func (m *MultiHostService) SubscribeEventsAndStats(ctx context.Context, events chan<- docker.ContainerEvent, stats chan<- docker.ContainerStat) {
	for _, client := range m.manager.List() {
		client.SubscribeEvents(ctx, events)
		client.SubscribeStats(ctx, stats)
	}
}

func (m *MultiHostService) SubscribeContainersStarted(ctx context.Context, containers chan<- docker.Container, filter ContainerFilter) {
	newContainers := make(chan docker.Container)
	for _, client := range m.manager.List() {
		client.SubscribeContainersStarted(ctx, newContainers)
	}
	go func() {
		<-ctx.Done()
		close(newContainers)
	}()

	go func() {
		for container := range newContainers {
			if filter(&container) {
				select {
				case containers <- container:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (m *MultiHostService) TotalClients() int {
	return len(m.manager.List())
}

func (m *MultiHostService) Hosts() []docker.Host {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	return m.manager.Hosts(ctx)
}

func (m *MultiHostService) LocalHost() (docker.Host, error) {
	for _, host := range m.Hosts() {
		if host.Type == "local" {
			return host, nil
		}
	}
	return docker.Host{}, fmt.Errorf("local host not found")
}

func (m *MultiHostService) SubscribeAvailableHosts(ctx context.Context, hosts chan<- docker.Host) {
	m.manager.Subscribe(ctx, hosts)
}
