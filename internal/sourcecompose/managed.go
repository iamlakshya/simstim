package sourcecompose

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/gitsource"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

// managedSource retains the actual original managed inventory, current complete
// inspections and native root identities until its native Git owner has joined.
// Manual/external shares are not represented as managed resources or inferred
// to be empty. Provider facts add refusals to the ONE canonical source walker.
type managedSource struct {
	mu sync.Mutex
	client.LocalGitSource
	environment []string
	hosts       []client.Host
	fingerprint string
	inspections []*provider.SourceInspection
	roots       []*os.File
	rootPaths   []string
	rootIDs     []string
	closed      bool
}

func managedUnknown() error {
	return &client.UserError{Code: "source_inventory", Msg: "the managed local writable-source inventory changed or is unknown", Hint: "Restore the original managed provider configuration and run the operation again.", Kind: client.KindRefused}
}

func managedHosts() ([]client.Host, string, error) {
	hosts, err := client.LoadHosts()
	if err != nil {
		return nil, "", err
	}
	var relevant []client.Host
	for _, h := range hosts {
		if h.Machine == nil || h.Machine.Provider != "docker" && h.Machine.Provider != "lima" {
			continue
		}
		machine := *h.Machine
		machine.Source = client.CloneMachineSource(machine.Source)
		relevant = append(relevant, client.Host{Name: h.Name, Machine: &machine})
	}
	slices.SortFunc(relevant, func(a, b client.Host) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	data, err := json.Marshal(relevant)
	return relevant, string(data), err
}

func inspectManaged(ctx context.Context, h client.Host, environment []string) (*provider.SourceInspection, error) {
	switch h.Machine.Provider {
	case "docker":
		return docker.InspectSource(ctx, h, environment)
	case "lima":
		return lima.InspectSource(ctx, h, environment)
	}
	return nil, managedUnknown()
}

// openManaged performs a complete actual read once, not once per walker step.
// The frozen restriction list never grants a read. Revalidate repeats complete
// inspection immediately before native start and refuses any changed stamp.
func openManaged(ctx context.Context, path string, graph bool, environment []string) (_ client.LocalGitSource, err error) {
	if environment == nil {
		return nil, managedUnknown()
	}
	hosts, fingerprint, err := managedHosts()
	if err != nil {
		return nil, err
	}
	s := &managedSource{environment: slices.Clone(environment), hosts: hosts, fingerprint: fingerprint}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	var restrictions []gitsource.Restriction
	for _, h := range hosts {
		inspection, inspectErr := inspectManaged(ctx, h, environment)
		if inspectErr != nil {
			return nil, inspectErr
		}
		s.inspections = append(s.inspections, inspection)
		if inspection == nil || inspection.Stamp == "" {
			return nil, managedUnknown()
		}
		for _, path := range inspection.Paths {
			canonical, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return nil, resolveErr
			}
			root, openErr := os.OpenRoot(filepath.Dir(canonical))
			if openErr != nil {
				return nil, openErr
			}
			info, openErr := root.Lstat(filepath.Base(canonical))
			if openErr != nil {
				root.Close()
				return nil, openErr
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				root.Close()
				return nil, managedUnknown()
			}
			var file *os.File
			if info.IsDir() {
				directory, dirErr := root.OpenRoot(filepath.Base(canonical))
				if dirErr != nil {
					root.Close()
					return nil, dirErr
				}
				file, openErr = directory.Open(".")
				openErr = errors.Join(openErr, directory.Close())
			} else {
				file, openErr = files.OpenRegular(root, filepath.Base(canonical))
			}
			closeErr := root.Close()
			if openErr != nil || closeErr != nil {
				if file != nil {
					closeErr = errors.Join(closeErr, file.Close())
				}
				return nil, errors.Join(openErr, closeErr)
			}
			s.roots = append(s.roots, file)
			identity, identityErr := files.Identity(file)
			if identityErr != nil {
				return nil, identityErr
			}
			s.rootPaths = append(s.rootPaths, canonical)
			s.rootIDs = append(s.rootIDs, identity)
			restrictions = append(restrictions, gitsource.Restriction{Path: canonical, Identity: identity, Direct: true})
		}
	}
	s.LocalGitSource, err = openPlatformSource(ctx, path, graph, restrictions)
	if err != nil {
		return nil, err
	}
	if err = s.revalidate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *managedSource) revalidate(ctx context.Context) error {
	_, fingerprint, err := managedHosts()
	if err != nil {
		return err
	}
	if fingerprint != s.fingerprint {
		return managedUnknown()
	}
	for index, h := range s.hosts {
		fresh, err := inspectManaged(ctx, h, s.environment)
		if err != nil {
			return err
		}
		if fresh == nil {
			return managedUnknown()
		}
		same := fresh.Stamp == s.inspections[index].Stamp && slices.Equal(fresh.Paths, s.inspections[index].Paths)
		closeErr := fresh.Close()
		if !same {
			return errors.Join(managedUnknown(), closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
		if err := s.inspections[index].Config.Revalidate(); err != nil {
			return err
		}
	}
	for index, path := range s.rootPaths {
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return err
		}
		info, err := root.Lstat(filepath.Base(path))
		if err != nil {
			root.Close()
			return err
		}
		var file *os.File
		if info.IsDir() {
			directory, openErr := root.OpenRoot(filepath.Base(path))
			if openErr != nil {
				root.Close()
				return openErr
			}
			file, err = directory.Open(".")
			err = errors.Join(err, directory.Close())
		} else if info.Mode().IsRegular() {
			file, err = files.OpenRegular(root, filepath.Base(path))
		} else {
			err = managedUnknown()
		}
		err = errors.Join(err, root.Close())
		if err != nil {
			if file != nil {
				err = errors.Join(err, file.Close())
			}
			return err
		}
		identity, err := files.Identity(file)
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		if identity != s.rootIDs[index] {
			return managedUnknown()
		}
	}
	return ctx.Err()
}

func (s *managedSource) Revalidate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return managedUnknown()
	}
	if err := s.revalidate(ctx); err != nil {
		return err
	}
	if err := s.LocalGitSource.Revalidate(ctx); err != nil {
		return err
	}
	return s.revalidate(ctx)
}

// CheckDestinations uses the SAME frozen additive restrictions and complete
// live inventory for a local copy's exact targets, with fresh provider checks.
func (s *managedSource) CheckDestinations(ctx context.Context, names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return managedUnknown()
	}
	if err := s.revalidate(ctx); err != nil {
		return err
	}
	checker, ok := s.LocalGitSource.(interface {
		CheckDestinations(context.Context, []string) error
	})
	if !ok {
		return managedUnknown()
	}
	if err := checker.CheckDestinations(ctx, names); err != nil {
		return err
	}
	return s.revalidate(ctx)
}
func (s *managedSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var failures []error
	if s.LocalGitSource != nil {
		failures = append(failures, s.LocalGitSource.Close())
	}
	for _, inspection := range s.inspections {
		failures = append(failures, inspection.Close())
	}
	for _, file := range s.roots {
		failures = append(failures, file.Close())
	}
	clear(s.environment)
	return errors.Join(failures...)
}
