package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
)

type joinedStagingProjectionSourceV1 struct {
	stage *InviteSSHStage
	route Host
}

func (*joinedStagingProjectionSourceV1) joinedProjectionV1() {}
func (s *joinedStagingProjectionSourceV1) OpenSSHNativeSource(ctx context.Context) (SSHNativeSourceLease, error) {
	if s == nil || s.stage == nil {
		return nil, hostStateRemovalError("the original joined staging source is missing")
	}
	lease := &joinedStagingProjectionLeaseV1{stage: s.stage, route: cloneObservationHost(s.route)}
	if err := lease.Revalidate(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}

type joinedStagingProjectionLeaseV1 struct {
	stage  *InviteSSHStage
	route  Host
	closed bool
}

func (s *joinedStagingProjectionLeaseV1) Revalidate(ctx context.Context) error {
	if s == nil || s.closed || s.stage == nil || s.stage.committed {
		return hostStateRemovalError("the original joined staging projection ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage := s.stage
	if stage.routeProjection != nil {
		if err := stage.routeProjection.check(ctx); err != nil {
			return err
		}
	}
	for _, child := range stage.children {
		info, err := os.Lstat(child.path)
		if err != nil || !os.SameFile(info, child.info) {
			return routeSourceErrorV1("an original staged route child changed")
		}
		data, err := os.ReadFile(child.path)
		if err != nil || !bytes.Equal(data, child.data) {
			return routeSourceErrorV1("original staged route bytes changed")
		}
	}

	if s.route.SSHConfig != stage.path || filepath.Dir(stage.path) != stage.directory || !stageProjectionAliasV1(stage, s.route) {
		return hostStateRemovalError("the joined staging projection has another route")
	}
	root, err := os.OpenRoot(stage.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Stat(".")
	if err != nil || !os.SameFile(stage.directoryInfo, directory) {
		return hostStateRemovalError("the original joined staging directory changed")
	}
	info, err := root.Lstat(filepath.Base(stage.path))
	if err != nil || !os.SameFile(stage.info, info) {
		return hostStateRemovalError("the original joined staging publisher changed")
	}
	data, err := root.ReadFile(filepath.Base(stage.path))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, stage.data) {
		return hostStateRemovalError("the original joined staging bytes changed")
	}
	return nil
}
func (s *joinedStagingProjectionLeaseV1) Close() error {
	if s != nil {
		s.closed = true
	}
	return nil
}
func (s *InviteSSHStage) readJoinedHopKeysV1(ctx context.Context, h Host, fingerprints []string) (keys []hostKey, failure error) {
	native, err := (&SSH{Host: h}).WithNativeSource(&joinedStagingProjectionSourceV1{stage: s, route: cloneObservationHost(h)})
	if err != nil {
		return nil, err
	}
	owned, finish, err := native.beginNativeSource(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { failure = joinRouteErrorsV1(failure, finish()) }()
	keys, _, failure = findHostKeySet(owned, h, "", fingerprints, FirstUseRefuse)
	return keys, failure
}

func stageProjectionAliasV1(stage *InviteSSHStage, route Host) bool {
	base := "joined-" + HostFileKey(route.Name)
	if route.SSH == base {
		return true
	}
	for index := range stage.hops {
		if route.SSH == base+"-hop-"+strconv.Itoa(index+1) {
			return true
		}
	}
	return false
}
