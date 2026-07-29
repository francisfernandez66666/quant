package registry

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type LazyService struct {
	once sync.Once
	svc  Service
	init func() (Service, error)
	err  error
}

func NewLazyService(init func() (Service, error)) *LazyService {
	return &LazyService{init: init}
}

func (l *LazyService) Get(ctx context.Context) (Service, error) {
	l.once.Do(func() {
		s, err := l.init()
		if err != nil {
			l.err = err
			return
		}
		if err = s.Start(ctx); err != nil {
			l.err = fmt.Errorf("lazy start %q: %w", s.Name(), err)
			return
		}
		l.svc = s
	})
	return l.svc, l.err
}

func (l *LazyService) Started() bool {
	return l.svc != nil
}

type Registry struct {
	mu       sync.RWMutex
	services map[string]Service
	lazy     map[string]*LazyService
	ordered  []Service
}

func New() *Registry {
	return &Registry{
		services: make(map[string]Service),
		lazy:     make(map[string]*LazyService),
	}
}

func (r *Registry) Register(svc Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.services[svc.Name()]; dup {
		log.Printf("registry: duplicate service %q — replacing", svc.Name())
	}
	r.services[svc.Name()] = svc
	r.ordered = append(r.ordered, svc)
}

func (r *Registry) RegisterLazy(name string, init func() (Service, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lazy[name] = NewLazyService(init)
}

func (r *Registry) StartCritical(ctx context.Context) error {
	t0 := time.Now()
	for _, svc := range r.ordered {
		if svc.Priority() != PriorityCritical {
			continue
		}
		if err := svc.Start(ctx); err != nil {
			return fmt.Errorf("critical start %q: %w", svc.Name(), err)
		}
	}
	log.Printf("registry: critical services ready (%v)", time.Since(t0))
	return nil
}

type StartError struct {
	Name string
	Err  error
}

func (r *Registry) StartBusiness(ctx context.Context, workers int) error {
	if workers <= 0 {
		workers = 4
	}
	t0 := time.Now()
	sem := make(chan struct{}, workers)
	errCh := make(chan StartError, len(r.ordered))
	var wg sync.WaitGroup

	for _, svc := range r.ordered {
		if svc.Priority() != PriorityBusiness {
			continue
		}
		wg.Add(1)
		go func(s Service) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := s.Start(ctx); err != nil {
				errCh <- StartError{Name: s.Name(), Err: err}
				<-sem
				return
			}
		}(svc)
	}
	wg.Wait()
	close(errCh)

	var errs []StartError
	for se := range errCh {
		errs = append(errs, se)
	}
	if len(errs) > 0 {
		for _, se := range errs {
			log.Printf("registry: business %q failed: %v", se.Name, se.Err)
		}
		return fmt.Errorf("%d business services failed", len(errs))
	}
	log.Printf("registry: business services ready (%v)", time.Since(t0))
	return nil
}

func (r *Registry) Get(ctx context.Context, name string) (Service, error) {
	r.mu.RLock()
	if svc, ok := r.services[name]; ok {
		r.mu.RUnlock()
		return svc, nil
	}
	lazy, ok := r.lazy[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: service %q not found", name)
	}
	return lazy.Get(ctx)
}

func (r *Registry) StopAll(ctx context.Context) {
	for _, svc := range r.ordered {
		if svc.Status() == StatusReady {
			_ = svc.Stop(ctx)
		}
	}
}

// Service returns the raw Service interface by name, or nil.
func (r *Registry) Service(name string) Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.services[name]
}

func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.services))
	for _, svc := range r.ordered {
		names = append(names, svc.Name())
	}
	return names
}
