package client

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"regexp"
	"strconv"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

type agentNativePeer struct {
	version, keyType, fingerprint string
	certificate                   bool
}

var agentNativeVersionRE = regexp.MustCompile(`^(?:debug1: )?OpenSSH_([0-9]+)\.([0-9]+)(?:p[0-9]+)?(?:[, ].*)?$`)

// readAgentNativePeer reads the whole synchronised regular prefix. Any
// differing record, malformed line or older contract refuses the proof.
func readAgentNativePeer(ctx context.Context, log *agentNativeLog) (agentNativePeer, error) {
	var peer agentNativePeer
	err := log.ScanSnapshot(ctx, func(r io.Reader) error {
		var err error
		peer, err = parseAgentNativePeer(r)
		return err
	})
	return peer, err
}

func parseAgentNativePeer(r io.Reader) (agentNativePeer, error) {
	var peer agentNativePeer
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	for scanner.Scan() {
		line := bytes.TrimRight(scanner.Bytes(), "\r")
		if match := agentNativeVersionRE.FindSubmatch(line); match != nil {
			version := string(match[1]) + "." + string(match[2])
			major, merr := strconv.Atoi(string(match[1]))
			minor, nerr := strconv.Atoi(string(match[2]))
			if merr != nil || nerr != nil || major < 8 || major == 8 && minor < 9 || peer.version != "" && peer.version != version {
				return peer, agentBindingProofError()
			}
			peer.version = version
		} else if bytes.HasPrefix(line, []byte("OpenSSH_")) || bytes.HasPrefix(line, []byte("debug1: OpenSSH_")) {
			return peer, agentBindingProofError()
		}
		keyType, fingerprint, certificate, keyLine, err := nativeAgentKeyLine(line)
		if err != nil {
			return peer, err
		}
		if keyLine {
			if peer.fingerprint != "" && (peer.fingerprint != string(fingerprint) || peer.keyType != string(keyType) || peer.certificate != certificate) {
				return peer, agentBindingProofError()
			}
			if peer.fingerprint == "" {
				peer.keyType, peer.fingerprint = string(keyType), string(fingerprint)
				peer.certificate = certificate
			}
		}
		// Native debug2_fr emits the operation name only on failure.
		if bytes.HasPrefix(line, []byte("debug2: ")) && bytes.Contains(line, []byte(": ssh_agent_bind_hostkey:")) {
			return peer, agentBindingProofError()
		}
	}
	if err := scanner.Err(); err != nil {
		return peer, agentBindingProofError()
	}
	if peer.version == "" || peer.keyType == "" || !fingerprintRE.MatchString(peer.fingerprint) {
		return peer, agentBindingProofError()
	}
	return peer, nil
}

// verifyNativeAgentRecord is called only after the collector has proved the
// native peer process, lifetime and lease. It does not accept host RPC data.
func verifyNativeAgentRecord(wire []byte, peer agentNativePeer) error {
	if len(wire) < 5 || len(wire) > agentFWMaxBody+4 || int(agentFirewallUint32(wire[:4])) != len(wire)-4 || wire[4] != agentFWExtension {
		return agentBindingProofError()
	}
	name, rest, ok := takeAgentFirewallString(wire[5:])
	if !ok || string(name) != agentFWSessionBind {
		return agentBindingProofError()
	}
	keyBlob, rest, ok := takeAgentFirewallString(rest)
	if !ok {
		return agentBindingProofError()
	}
	sessionID, rest, ok := takeAgentFirewallString(rest)
	if !ok || len(sessionID) == 0 {
		return agentBindingProofError()
	}
	signatureBlob, rest, ok := takeAgentFirewallString(rest)
	if !ok || len(rest) != 1 || rest[0] != 1 {
		return agentBindingProofError()
	}
	wireType, _, ok := takeAgentFirewallString(keyBlob)
	_, certificateFields := agentCertificateForm(string(wireType))
	if !ok || string(wireType) != peer.keyType || (certificateFields != 0) != peer.certificate {
		return agentBindingProofError()
	}
	key, keyType, err := parseAgentBindingKey(keyBlob)
	if err != nil || keyType != peer.keyType {
		return agentBindingProofError()
	}
	if ssh.FingerprintSHA256(key) != peer.fingerprint {
		return agentBindingProofError()
	}
	var signature ssh.Signature
	if err := ssh.Unmarshal(signatureBlob, &signature); err != nil {
		return agentBindingProofError()
	}
	if err := key.Verify(sessionID, &signature); err != nil {
		return agentBindingProofError()
	}
	return nil
}

func agentBindingProofError() error {
	return &UserError{Msg: "this SSH connection could not prove its native forwarding binding", Hint: "Check the SSH program and host identity, then attach again.", Code: "ssh_agent_refused", Kind: KindRefused}
}

func sealNativeAgentBinding(ctx context.Context, capture *agentBindingCollector, owner *Conn) (*peerAgentBinding, error) {
	if capture == nil || owner == nil || !owner.agentOnly || !ValidHostID(owner.HostID()) || owner.HostIDChanged() || !owner.HasFeature(agentbridge.Feature) {
		return nil, agentBindingProofError()
	}
	if err := capture.wait(ctx); err != nil {
		return nil, err
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	process, ok := owner.proc.(*sshProc)
	if !ok || process.rawDone == nil || capture.owner != process || capture.hostID != owner.HostID() || capture.ownerCtx == nil || capture.closed || capture.err != nil || len(capture.record) == 0 {
		return nil, agentBindingProofError()
	}
	select {
	case <-owner.Done():
		return nil, agentBindingProofError()
	case <-capture.ownerCtx.Done():
		return nil, agentBindingProofError()
	case <-process.rawExited():
		return nil, agentBindingProofError()
	default:
	}
	return &peerAgentBinding{wire: bytes.Clone(capture.record), owner: owner, scope: capture.scope, verified: true}, nil
}
