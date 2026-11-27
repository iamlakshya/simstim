package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	join "github.com/iamlakshya/simstim/internal/join/code"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HostInvite is the laptop's private name and labels for a public invite.
// Its key path contains a path chosen locally, never one read from a code.
type HostInvite struct {
	Version      int               `json:"version"`
	Name         string            `json:"name"`
	Labels       map[string]string `json:"labels,omitempty"`
	IdentityFile string            `json:"identity_file,omitempty"`
	PublicKey    string            `json:"public_key"`
	Nonce        uint64            `json:"nonce"`
	Expires      time.Time         `json:"expires"`
}

// InviteOptions selects the local name, public key and lifetime.
type InviteOptions struct {
	Name          string
	Labels        map[string]string
	PublicKeyFile string
	TTL           time.Duration
	Now           time.Time
}

func invitePath(name string) string {
	return filepath.Join(StateDir(), "invites", HostFileKey(name)+".json")
}
func inviteError(code, msg, hint string) error {
	return &UserError{Msg: msg, Hint: hint, Code: code, Kind: KindRefused}
}

// JoinError converts a closed join failure to a structured client error.
func JoinError(err error) error {
	if err == nil {
		return nil
	}
	var existing *UserError
	if errors.As(err, &existing) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var je *join.Error
	if errors.As(err, &je) {
		return inviteError(je.Code, je.Message, je.Hint)
	}
	return &UserError{Msg: "the machine operation failed: " + CleanLine(err.Error()), Hint: "Keep the root-owned leave script, check the reported change, then retry.", Code: "join_failed", Kind: KindFailed}
}

// CreateHostInvite makes one host's key and persists the invite before printing
// its public code. Existing unexpired invites and private keys are preserved.
func CreateHostInvite(ctx context.Context, o InviteOptions) (inv HostInvite, encoded string, failure error) {
	if !ValidHostName(o.Name) {
		return inv, "", badHostNameError(o.Name)
	}
	if o.TTL <= 0 {
		return inv, "", &UserError{Msg: "the invite lifetime must be above zero", Hint: "Use --ttl 1h, or another positive duration.", Code: "bad_value", Kind: KindUsage}
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	unlock, err := lockInvite(ctx, o.Name)
	if err != nil {
		return inv, "", err
	}
	defer unlock()
	hosts, err := LoadHosts()
	if err != nil {
		return inv, "", err
	}
	for _, h := range hosts {
		if h.Name == o.Name {
			return inv, "", inviteError("host_exists", "that host is already saved", "Choose another name, or remove the saved host first.")
		}
	}
	if old, err := readHostInvite(o.Name); err == nil && o.Now.Before(old.Expires) {
		return inv, "", inviteError("invite_exists", "an open invite already uses that name", "Wait for it to expire, or use another host name.")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return inv, "", err
	}
	inv = HostInvite{Version: 1, Name: o.Name, Labels: o.Labels, Expires: o.Now.Add(o.TTL).UTC().Truncate(time.Second)}
	var n [8]byte
	for inv.Nonce == 0 {
		if _, err = rand.Read(n[:]); err != nil {
			return inv, "", fmt.Errorf("make the invite nonce: %w", err)
		}
		inv.Nonce = binary.BigEndian.Uint64(n[:])
	}
	var created *inviteIdentityBirth
	if o.PublicKeyFile != "" {
		b, e := os.ReadFile(expand(o.PublicKeyFile))
		if e != nil {
			return inv, "", fmt.Errorf("read the invite public key: %w", e)
		}
		if len(b) > 16<<10 {
			return inv, "", inviteError("code_check", "the public key file is too large", "Use a file holding one SSH public key.")
		}
		inv.PublicKey, err = join.PublicKey(strings.TrimSpace(string(b)))
	} else {
		inv.IdentityFile = filepath.Join(ConfigDir(), "ssh", "id-"+HostFileKey(o.Name))
		inv.PublicKey, created, err = inviteIdentity(inv.IdentityFile)
	}
	if err != nil {
		return inv, "", JoinError(err)
	}
	saved := false
	defer func() {
		if !saved && created != nil {
			failure = errors.Join(failure, created.remove())
		}
	}()
	code, err := join.EncodeInvite(join.Invite{PublicKey: inv.PublicKey, Nonce: inv.Nonce, Expires: inv.Expires})
	if err != nil {
		return inv, "", JoinError(err)
	}
	if err = ctx.Err(); err != nil {
		return inv, "", err
	}
	path := invitePath(o.Name)
	if err = privateInviteDir(filepath.Dir(path)); err != nil {
		return inv, "", err
	}
	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return inv, "", err
	}
	b = append(b, '\n')
	if len(b) > 64<<10 {
		return inv, "", &UserError{Msg: "the invite's labels make its record too large", Hint: "Use fewer or shorter labels.", Code: "bad_value", Kind: KindUsage}
	}
	if err = writeAtomicFile(path, ".invite-*", b); err != nil {
		return inv, "", err
	}
	saved = true
	return inv, code, nil
}
func privateInviteDir(dir string) error {
	root, err := cachefs.OpenPrivateRoot(dir, true)
	if err != nil {
		return inviteError("invite_store", "the invite folder is not an owned private folder", "Restore its owner-only permissions or ACLs, then retry.")
	}
	return root.Close()
}
func inviteIdentity(path string) (public string, created *inviteIdentityBirth, err error) {
	if err = privateInviteDir(filepath.Dir(path)); err != nil {
		return
	}
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() || st.Size() > 64<<10 {
			return "", nil, inviteError("invite_key", "the host's existing private key is not private", "Protect the host's key with mode 0600 and retry.")
		}
		root, e := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
		if e != nil {
			return "", nil, e
		}
		defer root.Close()
		file, e := cachefs.OpenFile(root, filepath.Base(path), os.O_RDONLY, 0o600)
		if e != nil {
			return "", nil, e
		}
		b, e := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		e = errors.Join(e, file.Close())
		if e != nil {
			return "", nil, e
		}
		if len(b) > 64<<10 {
			return "", nil, inviteError("invite_key", "the existing key is too large", "Use one SSH private key file.")
		}

		key, e := ssh.ParsePrivateKey(b)
		if e != nil {
			return "", nil, inviteError("invite_key", "the host's existing key cannot be read", "Use --key with an SSH public key file, or move the old key aside.")
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))), nil, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", nil, e
	}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return "", nil, e
	}
	block, e := ssh.MarshalPrivateKey(priv, brand.Name+" host key")
	if e != nil {
		return "", nil, e
	}
	key, e := ssh.NewPublicKey(pub)
	if e != nil {
		return "", nil, e
	}
	root, e := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
	if e != nil {
		return "", nil, e
	}
	defer root.Close()
	directory, e := cachefs.CaptureRoot(root)
	if e != nil {
		return "", nil, e
	}
	f, e := cachefs.OpenFile(root, filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return "", nil, e
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return "", nil, e
	}
	birth := &inviteIdentityBirth{path: path, info: info, directory: directory}
	_, e = f.Write(pem.EncodeToMemory(block))
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return "", nil, errors.Join(e, birth.remove())
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), birth, nil
}

func readHostInvite(name string) (HostInvite, error) {
	var inv HostInvite
	if !ValidHostName(name) {
		return inv, badHostNameError(name)
	}
	path := invitePath(name)
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
	if err != nil {
		return inv, err
	}
	defer root.Close()
	file, err := cachefs.OpenFile(root, filepath.Base(path), os.O_RDONLY, 0o600)
	if err != nil {
		return inv, err
	}
	b, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return inv, err
	}
	if len(b) > 64<<10 {
		return inv, inviteError("invite_store", "the invite record is too large", "Make a fresh invite under another name.")
	}

	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&inv); err != nil {
		return inv, inviteError("invite_store", "the invite record cannot be read", "Make a fresh invite under another name.")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return inv, inviteError("invite_store", "the invite record has trailing data", "Make a fresh invite under another name.")
	}
	if inv.Version != 1 || inv.Name != name || inv.Nonce == 0 || inv.Expires.IsZero() || inv.Expires.Unix() <= 0 {
		return inv, inviteError("invite_store", "the invite record is invalid", "Make a fresh invite under another name.")
	}
	if _, err := join.PublicKey(inv.PublicKey); err != nil {
		return inv, JoinError(err)
	}
	return inv, nil
}

// InviteAcceptance owns an invite's separate cross-process lock through save.
// A caller must Close on every path, and Consume only after registration saved.
type InviteAcceptance struct {
	Invite     HostInvite
	original   HostInvite
	reply      join.Reply
	projection *InviteSSHStage
	ctx        context.Context
	unlock     func()
	consumed   bool
	registered *HostPrepareTargetV1
	expected   Host
	stage      *InviteSSHStage
	capture    *hostPrepareCommitCapture
}

// Context carries the original invite owner without a registration OP or dial.
func (a *InviteAcceptance) Context() context.Context {
	return context.WithValue(a.ctx, inviteAcceptanceKey{}, a)
}

type inviteAcceptanceKey struct{}

// BindSSH binds the locally staged config to the original acceptance before
// registration. It carries no host connection or saved-source authority.
func (a *InviteAcceptance) BindSSH(h Host, stage *InviteSSHStage) error {
	if a == nil || stage == nil || stage.acceptance != a || a.projection != stage || !SameEntry(h, stage.originalHost) {
		return hostStateRemovalError("the joined config has no matching original acceptance producer")
	}
	if err := a.checkOriginalInviteV1(a.Context()); err != nil {
		return err
	}
	a.expected = cloneObservationHost(h)
	a.stage = stage
	return nil
}

// Registered reports the actual writer-derived commit of this acceptance.
func (a *InviteAcceptance) Registered() bool { return a.registered != nil }

// CheckInviteRegistrationV1 checks the actual registration change immediately
// before its existing native writer. An invite cannot replace an existing host.
func CheckInviteRegistrationV1(ctx context.Context, change HostChange) error {
	a, _ := ctx.Value(inviteAcceptanceKey{}).(*InviteAcceptance)
	if a == nil {
		return nil
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if err := a.checkOriginalInviteV1(ctx); err != nil {
		return err
	}
	if current, err := readHostInvite(a.original.Name); err != nil || current.Nonce != a.original.Nonce {
		return inviteError("invite_unknown", "the original invite changed before registration", "Make a fresh invite on the laptop.")
	}
	if change.Was != nil || change.Now == nil || !SameEntry(*change.Now, a.expected) {
		return hostStateRemovalError("an invite requires its original new-host registration")
	}
	if !time.Now().Before(a.original.Expires) {
		return inviteError("invite_expired", "the invite expired before registration was saved", "Make a fresh invite on the laptop.")
	}
	return nil
}

// RecordInviteRegistrationV1 consumes an invite only from the actual original
// successful registration writer. Ordinary additions have no invite and return.
func RecordInviteRegistrationV1(ctx context.Context, target *HostPrepareTargetV1) error {
	a, _ := ctx.Value(inviteAcceptanceKey{}).(*InviteAcceptance)
	if a == nil {
		return nil
	}
	if a.unlock == nil || a.consumed {
		return hostStateRemovalError("the original invite owner is closed")
	}
	if target == nil || !target.FromRegistration() || target.Name() != a.original.Name || a.stage == nil || !SameEntry(target.Host(), a.expected) {
		return hostStateRemovalError("the invite has no matching original registration commit")
	}
	// This exact object proves that the real writer already committed the
	// original config. Preserve it even if a later Source change makes the
	// current-state check refuse further work or invite consumption.
	a.registered = target
	a.stage.Commit()
	if err := target.Check(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return a.Consume()
}

// Close releases ownership without consuming a failed or declined invitation.
func (a *InviteAcceptance) Close() {
	if a.unlock != nil {
		a.unlock()
		a.unlock = nil
	}
}

// Consume removes only this original invite after the saved host exists.
func (a *InviteAcceptance) Consume() error {
	if a.consumed {
		return nil
	}
	current, err := readHostInvite(a.original.Name)
	if err != nil {
		return err
	}
	if !sameHostInviteV1(current, a.original) {
		return inviteError("invite_unknown", "the invite changed while the host was added", "Inspect the saved host before making another invite.")
	}
	if a.registered == nil {
		return inviteError("invite_unknown", "the original registration writer did not save this invite", "Retry accepting the reply.")
	}
	if err = a.registered.Check(context.WithoutCancel(a.ctx)); err != nil {
		return err
	}
	if err = os.Remove(invitePath(a.original.Name)); err != nil {
		return fmt.Errorf("remove the accepted invite: %w", err)
	}
	a.consumed = true
	return nil
}

// BeginInviteAcceptance validates the entire reply, nonce, expiry and local
// name without changing any pin or host. The name never comes from the reply.
func BeginInviteAcceptance(ctx context.Context, name string, r join.Reply, now time.Time) (*InviteAcceptance, error) {
	if !ValidHostName(name) {
		return nil, badHostNameError(name)
	}
	if err := r.Validate(); err != nil {
		return nil, JoinError(err)
	}
	if now.IsZero() {
		now = time.Now()
	}
	initial, initialErr := readHostInvite(name)
	if errors.Is(initialErr, os.ErrNotExist) || initialErr == nil && initial.Nonce != r.Nonce {
		return nil, inviteError("invite_unknown", "the reply matches no open invite for that host", brand.Name+" host rm "+name+", then "+brand.Name+" host invite "+name+".")
	}
	if initialErr != nil {
		return nil, initialErr
	}
	if !now.Before(initial.Expires) {
		return nil, inviteError("invite_expired", "the invite has expired", "Make another invite on the laptop.")
	}
	unlock, err := lockInvite(ctx, name)
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			unlock()
		}
	}()
	inv, err := readHostInvite(name)
	if errors.Is(err, os.ErrNotExist) || err == nil && inv.Nonce != r.Nonce {
		return nil, inviteError("invite_unknown", "the reply matches no open invite for that host", brand.Name+" host rm "+name+", then "+brand.Name+" host invite "+name+".")
	}
	if err != nil {
		return nil, err
	}
	if !now.Before(inv.Expires) {
		return nil, inviteError("invite_expired", "the invite has expired", "Make another invite on the laptop.")
	}
	release = false
	return &InviteAcceptance{Invite: cloneHostInviteV1(inv), original: cloneHostInviteV1(inv), reply: cloneJoinedReplyV1(r), ctx: ctx, unlock: unlock}, nil
}
