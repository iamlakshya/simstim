package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func DecodePolicyLockdownV1(raw json.RawMessage) (api.PolicyLockdownArgsV1, error) {
	var args api.PolicyLockdownArgsV1
	fields, err := api.JSONObjectFields(raw, api.ExactJSONFieldNames(args)...)
	if err != nil || !api.ExactJSONFields(raw, args) || fields["v"] == nil || fields["on"] == nil || bytes.Equal(bytes.TrimSpace(fields["v"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(fields["on"]), []byte("null")) {
		return args, api.ErrPolicyLockdownV1
	}
	for _, name := range []string{"session", "previous_hash"} {
		if value, exists := fields[name]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, api.ErrPolicyLockdownV1
		}
	}
	if json.Unmarshal(raw, &args) != nil || args.Validate() != nil {
		return args, api.ErrPolicyLockdownV1
	}
	return args, nil
}

func (b *bridge) handlePolicyLockdownV1(req proto.Request) bool {
	if req.Op != "policy.lockdown" {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	args, err := DecodePolicyLockdownV1(req.Args)
	if err != nil || args.Session != "" {
		b.reply(req.ID, nil, UserError("bad_request", "lockdown requires one supported host-wide setting", "Use policy lockdown with the current saved host."))
		return true
	}
	result, err := SetHostPolicyLockdownContextV1(b.ctx, args.On)
	b.reply(req.ID, result, err)
	return true
}

// SetHostPolicyLockdownContextV1 commits the host setting, then negotiates the
// actual current supervisor of every primary. No local laptop layers enter it.
func SetHostPolicyLockdownContextV1(ctx context.Context, on bool) (api.HostLockdownResultV1, error) {
	result := api.HostLockdownResultV1{Version: 1, On: on, Sessions: []api.PolicyLockdownResultV1{}, Unreached: []string{}}
	if ctx == nil {
		return result, api.ErrPolicyLockdownV1
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := SetPersistentLockdownContextV1(ctx, on); err != nil {
		return result, err
	}
	ids, err := lockdownPrimaryIDsV1(ctx)
	if err != nil {
		return result, err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	semaphore := make(chan struct{}, 8)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				result.Unreached = append(result.Unreached, id)
				mu.Unlock()
				return
			}
			defer func() { <-semaphore }()
			ack, err := pushHostLockdownToSupervisorV1(ctx, id, on)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Unreached = append(result.Unreached, id)
			} else {
				result.Sessions = append(result.Sessions, ack)
			}
		}(id)
	}
	wg.Wait()
	slices.Sort(result.Unreached)
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].Session < result.Sessions[j].Session })
	if len(result.Unreached) != 0 {
		return result, UserError("policy_unreached", "lockdown could not reach every sandbox on this host", "Keep the host recorded and retry policy lockdown after the unreachable sandbox is ready.")
	}
	return result, ctx.Err()
}

func lockdownPrimaryIDsV1(ctx context.Context) ([]string, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	ids := []string{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := directory.ReadDir(128)
		for _, entry := range entries {
			if !entry.IsDir() || !api.ValidSessionID(entry.Name()) {
				continue
			}
			manifest, readErr := LoadManifest(entry.Name())
			if readErr != nil {
				return nil, UserError("policy_read", "a sandbox's protected record could not be checked for lockdown", "Keep the record and inspect the host before retrying lockdown.")
			}
			if manifest.SandboxOf != "" || manifest.Wall == nil {
				continue
			}
			if !alive(manifest.ID) && manifest.Status.State != StateStarting && (manifest.Life == nil || manifest.Life.Phase != LifeBooting) {
				continue
			}
			ids = append(ids, manifest.ID)
			if len(ids) > 4096 {
				return nil, UserError("policy_unreached", "there are too many sandboxes for one lockdown response", "Apply lockdown on smaller host scopes and inspect the host's session records.")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func lockdownHolderWaitV1(ctx context.Context) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	wait := 5 * time.Second
	if deadline, exists := ctx.Deadline(); exists {
		wait = min(wait, time.Until(deadline))
	}
	if wait <= 0 {
		return 0, context.DeadlineExceeded
	}
	return wait, nil
}

func pushHostLockdownToSupervisorV1(ctx context.Context, id string, on bool) (api.PolicyLockdownResultV1, error) {
	var empty api.PolicyLockdownResultV1
	for range 3 {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		current, err := LoadManifest(id)
		if err != nil {
			return empty, err
		}
		if current.ID != id || current.SandboxOf != "" || current.Wall == nil {
			return empty, api.ErrPolicyLockdownV1
		}
		before, err := api.HashPolicyLayerVectorV1(current.Layers)
		if err != nil {
			return empty, err
		}
		_, desired, err := ApplyPolicyLockdownLayersV1(current.Layers, on)
		if err != nil {
			return empty, err
		}
		expected, err := api.HashPolicyLayerVectorV1(desired)
		if err != nil {
			return empty, err
		}
		wait, err := lockdownHolderWaitV1(ctx)
		if err != nil {
			return empty, err
		}
		args := api.PolicyLockdownArgsV1{Version: 1, On: on, Session: id, PreviousHash: before}
		raw, err := HolderRequestWithFeatures(id, "policy.lockdown", args, wait, api.FeaturePolicyLockdownV1)
		if err != nil {
			return empty, err
		}
		var result api.PolicyLockdownResultV1
		if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Validate() != nil || result.Session != id || result.On != on {
			return empty, api.ErrPolicyLockdownV1
		}
		if result.State == "conflict" {
			continue
		}
		if result.Hash != expected {
			return empty, api.ErrPolicyLockdownV1
		}
		return result, nil
	}
	return empty, UserError("policy_conflict", "the sandbox's policy changed during three lockdown attempts", "Retry lockdown against its current actor.")
}
