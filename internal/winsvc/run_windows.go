//go:build windows

package winsvc

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows/svc"
)

type handler struct {
	name string
	work func(ctx context.Context) error
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- h.work(ctx) }()

	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-errCh:
			cancel()
			if err != nil {
				s <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				s <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
			}
		}
	}
}

func run(name string, work func(ctx context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("checking service context: %w", err)
	}
	if !isService {
		// Foreground dev-run on Windows (e.g. `scvmm-poller.exe -console`).
		return work(context.Background())
	}
	return svc.Run(name, &handler{name: name, work: work})
}
