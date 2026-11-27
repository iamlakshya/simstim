package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const FeaturePolicyLockdownV1 = "policy-lockdown-v1"

// PolicyLockdownArgsV1 changes only the host's lockdown restriction. A session
// request names the exact current protected source vector for its CAS.
type PolicyLockdownArgsV1 struct {
	Version      int    `json:"v"`
	On           bool   `json:"on"`
	Session      string `json:"session,omitempty"`
	PreviousHash string `json:"previous_hash,omitempty"`
}

type PolicyLockdownResultV1 struct {
	Version int    `json:"v"`
	On      bool   `json:"on"`
	Session string `json:"session"`
	State   string `json:"state"`
	Hash    string `json:"hash"`
}

type HostLockdownResultV1 struct {
	Version   int                      `json:"v"`
	On        bool                     `json:"on"`
	Sessions  []PolicyLockdownResultV1 `json:"sessions"`
	Unreached []string                 `json:"unreached"`
}

var ErrPolicyLockdownV1 = errors.New("invalid lockdown operation. Try: use the current saved host connection and matching policy helpers")

func (args PolicyLockdownArgsV1) Validate() error {
	if args.Version != 1 || args.Session != "" && (!ValidSessionID(args.Session) || !ValidPolicyHashV1(args.PreviousHash)) || args.Session == "" && args.PreviousHash != "" {
		return ErrPolicyLockdownV1
	}
	return nil
}

func (result PolicyLockdownResultV1) Validate() error {
	if result.Version != 1 || !ValidSessionID(result.Session) || !ValidPolicyHashV1(result.Hash) || result.State != "applied" && result.State != "unchanged" && result.State != "conflict" {
		return ErrPolicyLockdownV1
	}
	return nil
}

func (result HostLockdownResultV1) Validate() error {
	if result.Version != 1 || result.Sessions == nil || result.Unreached == nil || len(result.Sessions)+len(result.Unreached) > 4096 {
		return ErrPolicyLockdownV1
	}
	seen := map[string]bool{}
	for _, session := range result.Sessions {
		if session.Validate() != nil || session.On != result.On || session.State == "conflict" || seen[session.Session] {
			return ErrPolicyLockdownV1
		}
		seen[session.Session] = true
	}
	for _, session := range result.Unreached {
		if !ValidSessionID(session) || seen[session] {
			return ErrPolicyLockdownV1
		}
		seen[session] = true
	}
	return nil
}

// HashPolicyLayerVectorV1 hashes data for a current-actor CAS, including the
// excluded session and lockdown layers. It grants no allowance or readiness.
func HashPolicyLayerVectorV1(layers []Layer) (string, error) {
	for _, layer := range layers {
		if !ValidLayerRef(LayerRef{Kind: layer.Kind, Name: layer.Name, Source: layer.Source}) {
			return "", ErrPolicyLockdownV1
		}
	}
	raw, err := json.Marshal(CloneLayers(layers))
	if err != nil {
		return "", ErrPolicyLockdownV1
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
