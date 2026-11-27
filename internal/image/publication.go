package image

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// PublicationGate runs publish while the host's placement/control gate remains
// held. Callers enter with the image lease first. Runtime store locks, if any,
// come last. The callback must not run network, conversion or user prompts.
type PublicationGate func(context.Context, func() error) error

// OpenWithPublicationGate anchors a store whose actual stable namespace and
// metadata publications pass through gate exactly once per publication. Host
// consumers reserve active operations before entering P32 and never enter it
// while already holding placement. Staging and ownership intents are ungated.
func OpenWithPublicationGate(dir string, gate PublicationGate) (*Store, error) {
	return openDomainStore(context.Background(), dir, cachefs.Domain{}, gate, true)
}

func (s *Store) withPublication(ctx context.Context, publish func() error) error {
	if err := s.requireWritableHost(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.generation.Known() {
		if err := s.generation.CheckRoot(s.root); err != nil {
			return err
		}
	}
	if s.gate != nil {
		return s.gate(ctx, publish)
	}
	return publish()
}

func (s *Store) exclusiveDirectory(name string) (*os.Root, error) {
	if err := s.root.Mkdir(name, 0o700); err != nil {
		return nil, err
	}
	return cachefs.OpenPrivateChild(s.root, name, false)
}

// OpenExisting anchors existing image storage for inventory or total purge.
// It creates no root, infrastructure directory or lease. Host purge uses this
// constructor and retains its external drain/operation guard until completion.
func OpenExisting(dir string) (*Store, error) {
	return openDomainStore(context.Background(), dir, cachefs.Domain{}, nil, false)
}
