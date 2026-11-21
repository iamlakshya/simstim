package client

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// PrepareNativeResume retains a genuine warm continuation or a cold native
// absence and complete memory reservation before any resume key command.
// Its result describes that same connection's owner; it grants no authority.
func (c *Conn) PrepareNativeResume(ctx context.Context, a ResumeArgs) (string, error) {
	ctx, _, err := captureResumeImagePolicy(ctx, c, a.ID)
	if err != nil {
		return "", err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	image := r.resumeImage
	r.mu.Unlock()
	ctx, a, err = image.arguments(ctx, a)
	if err != nil {
		return "", err
	}
	n, err := prepareResumeNamedSSHKeys(ctx, c, a.ID)
	if err != nil {
		return "", err
	}
	if n == nil {
		return "", nil
	}
	owned, finish, err := n.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	prepared, err := n.prepareNative(owned, c, a)
	if err != nil {
		return "", err
	}
	return prepared.Mode, nil
}

func (n *resumeNamedSSHKeyPlan) metadataBytes(a ResumeArgs) ([]byte, error) {
	if a.Snapshot == nil {
		n.request.mu.Lock()
		if group := n.request.resume; group != nil {
			a.Snapshot = cloneGrantSnapshot(group.snapshot)
		}
		n.request.mu.Unlock()
	}
	metadata, err := api.ResumePreparationMetadataV1(a)
	if err != nil {
		return nil, err
	}
	metadata.Layers = api.ProjectClientImagePolicy(api.ProjectClientInputSources(metadata.Layers))
	return json.Marshal(metadata)
}

func (n *resumeNamedSSHKeyPlan) prepareNative(ctx context.Context, c *Conn, a ResumeArgs) (*api.ResumePrepareResultV1, error) {
	if originalRewindV1(ctx) != nil {
		return nil, rewindSourceErrorV1()
	}
	if err := n.check(ctx, c, false); err != nil {
		return nil, err
	}
	metadata, err := n.metadataBytes(a)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	prepared := n.prepared
	before := bytes.Clone(n.metadata)
	n.mu.Unlock()
	if prepared == nil {
		var start ResumeArgs
		if err := json.Unmarshal(metadata, &start); err != nil {
			return nil, err
		}
		n.mu.Lock()
		n.nativeAttempted = true
		n.mu.Unlock()
		var result api.ResumePrepareResultV1
		if err := c.Call(resumePreparationRPCContextV1(ctx), "resume.prepare-v1", api.ResumePrepareArgsV1{V: 1, Start: start}, &result); err != nil {
			return nil, err
		}
		if !result.Valid() {
			return nil, namedSSHKeySourceError()
		}
		primary := localSandboxRoot(n.creation)
		if result.Primary != primary {
			return nil, namedSSHKeySourceError()
		}
		n.mu.Lock()
		n.prepared = &result
		n.metadata = bytes.Clone(metadata)
		prepared = n.prepared
		n.mu.Unlock()
	} else if !bytes.Equal(before, metadata) {
		return nil, namedSSHKeySourceError()
	}

	if err := n.checkRemote(ctx); err != nil {
		return nil, err
	}
	if prepared.Mode == "warm" && a.MemoryMax != 0 {
		n.mu.Lock()
		emit := !n.memoryWarned
		n.memoryWarned = true
		n.mu.Unlock()
		if emit {
			warn(ctx, "resume_memory_ignored", "The sandbox is already running, so --memory-max leaves its current memory cap unchanged.", "Stop the sandbox before choosing a new memory cap.")
		}
	}
	return prepared, nil
}

// retainedNativeResumeModeV1 consumes only the actual same R/Conn/Source plan.
// It is called before fetching values, never from a held physical writer turn.
func retainedNativeResumeModeV1(ctx context.Context, c *Conn, id string) (string, error) {
	if rewind := originalRewindV1(ctx); rewind != nil {
		return rewind.preparationModeV1(ctx, c, id, true)
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return "", namedSSHKeySourceError()
	}
	r.mu.Lock()
	n := r.namedResume
	r.mu.Unlock()
	if n == nil || n.conn != c || n.id != id {
		return "", namedSSHKeySourceError()
	}
	if err := n.checkRemote(ctx); err != nil {
		return "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.prepared == nil || !n.prepared.Valid() {
		return "", namedSSHKeySourceError()
	}
	return n.prepared.Mode, nil
}

// retainedNativeResumeModeLocalV1 validates only local retained source. It is
// safe inside the physical writer turn and never sends a nested RPC.
func retainedNativeResumeModeLocalV1(ctx context.Context, c *Conn, id string) (string, error) {
	if rewind := originalRewindV1(ctx); rewind != nil {
		return rewind.preparationModeV1(ctx, c, id, false)
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return "", namedSSHKeySourceError()
	}
	r.mu.Lock()
	n := r.namedResume
	r.mu.Unlock()
	if n == nil || n.conn != c || n.id != id {
		return "", namedSSHKeySourceError()
	}
	if err := n.check(ctx, c, false); err != nil {
		return "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.prepared == nil || !n.prepared.Valid() {
		return "", namedSSHKeySourceError()
	}
	return n.prepared.Mode, nil
}

// Preparation has its own exact request. Keep the same Source and image use,
// while reserving the final image publication guard for the resume frame.
func resumePreparationRPCContextV1(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, resumeImagePublicationKeyV1{}, (*resumeImagePolicyV1)(nil))
	return context.WithValue(ctx, resumeNamedSSHKeyPublicationKey{}, (*resumeNamedSSHKeyPlan)(nil))
}
