package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type placementsV1 struct {
	Version int                 `json:"v"`
	Records []placementRecordV1 `json:"records"`
}

type placementRecordV1 struct {
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	HostID     string    `json:"host_id"`
	Generation string    `json:"source_generation"`
	At         time.Time `json:"at"`
	Memory     int64     `json:"declared"`
	Reason     string    `json:"reason"`
	Session    string    `json:"session,omitempty"`
	Pending    bool      `json:"pending,omitempty"`
}

// ownedPlacementRecord is local in-flight accounting only. Its creator may
// update/remove only the exact original record. It grants no host capacity.
type ownedPlacementRecord struct {
	store  *placementStore
	record placementRecordV1
}

func decodePlacements(raw []byte) (placementsV1, error) {
	var state placementsV1
	fields, err := api.JSONObjectFields(raw, "v")
	if err != nil || json.Unmarshal(fields["v"], &state.Version) != nil {
		return state, placementStoreError(errPlacementState)
	}
	if state.Version != 1 {
		return state, hostFactsFutureError()
	}
	if !api.ExactJSONFields(raw, state) || json.Unmarshal(raw, &state) != nil || state.Records == nil {
		return state, placementStoreError(errPlacementState)
	}
	seen := make(map[string]bool, len(state.Records))
	for _, record := range state.Records {
		if !ValidHostID(record.ID) || seen[record.ID] || !ValidHostName(record.Host) || !ValidHostID(record.HostID) || !ValidHostID(record.Generation) || record.At.IsZero() || record.Memory <= 0 ||
			record.Reason == "" || api.CleanText(record.Reason, 256) != record.Reason || record.Session != "" && !api.ValidSessionID(record.Session) || record.Pending && record.Session == "" {
			return state, placementStoreError(errPlacementState)
		}
		seen[record.ID] = true
	}
	return state, nil
}

func (s *placementStore) readPlacements(ctx context.Context) (placementsV1, error) {
	raw, exists, err := s.read(ctx, "placements.json")
	if err != nil {
		return placementsV1{}, err
	}
	if !exists {
		return placementsV1{Version: 1, Records: []placementRecordV1{}}, nil
	}
	return decodePlacements(raw)
}

func recentPlacementRecords(records []placementRecordV1, now time.Time) []placementRecordV1 {
	return slices.DeleteFunc(records, func(record placementRecordV1) bool { return now.Sub(record.At) >= 10*time.Minute })
}

func placementInFlight(records []placementRecordV1, observation *hostFactsObservation, now time.Time, fences ...*placementCompletionFenceV1) (int64, error) {
	if observation == nil || len(fences) > 1 {
		return 0, placementStoreError(errPlacementState)
	}
	var fence *placementCompletionFenceV1
	if len(fences) == 1 {
		fence = fences[0]
	}
	return placementResidualV1(records, observation.host.Name, observation.hostID, observation.source.generation, observation.facts, observation, fence, now)
}

func (s *placementStore) placementHistory(ctx context.Context, observations []*hostFactsObservation) (map[string]int64, []string, error) {
	inFlight := make(map[string]int64, len(observations))
	var recent []string
	err := s.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := s.readPlacements(owned)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, observation := range observations {
			amount, err := placementInFlight(state.Records, observation, now)
			if err != nil {
				return err
			}
			inFlight[observation.host.Name] = amount
		}
		for _, record := range state.Records {
			if age := now.Sub(record.At); age >= 0 && age < 10*time.Minute {
				recent = append(recent, record.Host)
			}
		}
		return nil
	})
	return inFlight, recent, err
}

func (s *placementStore) reservePlacement(ctx context.Context, candidate *autoPlacementCandidate, reason string) (*ownedPlacementRecord, error) {
	if candidate == nil || candidate.observation == nil || candidate.declared <= 0 || reason == "" || api.CleanText(reason, 256) != reason {
		return nil, placementStoreError(errPlacementState)
	}
	if err := candidate.observation.check(ctx); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, placementStoreError(err)
	}
	observation := candidate.observation
	record := placementRecordV1{ID: hex.EncodeToString(nonce[:]), Host: observation.host.Name, HostID: observation.hostID,
		Generation: observation.source.generation, At: time.Now().UTC(), Memory: candidate.declared, Reason: reason}
	ownedRecord := &ownedPlacementRecord{store: s, record: record}
	publicationAttempted := false
	err := s.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := s.readPlacements(owned)
		if err != nil {
			return err
		}
		reserved, err := placementInFlight(state.Records, observation, record.At, candidate.placementFence)
		if err != nil {
			return err
		}
		facts := observation.facts
		if !facts.MemoryKnown || !facts.AccountingKnown || record.Memory > facts.Room || reserved > facts.Room-record.Memory {
			return &placementReservationRefusalV1{actual: &UserError{Msg: "another local placement used this host's observed room",
				Hint: "Refresh host facts and choose the next eligible host.", Code: "no_room", Kind: KindRefused}}
		}
		state.Records = recentPlacementRecords(state.Records, record.At)
		state.Records = append(state.Records, record)
		publicationAttempted = true
		return s.write(owned, "placements.json", state)
	})
	if err != nil && !publicationAttempted {
		var refusal *placementReservationRefusalV1
		if errors.As(err, &refusal) {
			refusal.store, refusal.candidate = s, candidate
		}
		return nil, err
	}
	return ownedRecord, err
}

func (r *ownedPlacementRecord) finish(ctx context.Context, session string, refused bool) error {
	if r == nil || r.store == nil || !refused && !api.ValidSessionID(session) {
		return placementStoreError(errPlacementState)
	}
	return r.store.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := r.store.readPlacements(owned)
		if err != nil {
			return err
		}
		at := slices.IndexFunc(state.Records, func(record placementRecordV1) bool { return record.ID == r.record.ID })
		if at < 0 {
			return placementStoreError(errPlacementState)
		}
		if !samePlacementRecordV1(state.Records[at], r.record) || refused && r.record.Session != "" && !r.record.Pending {
			return placementStoreError(errPlacementState)
		}
		if refused {
			state.Records = slices.Delete(state.Records, at, at+1)
		} else {
			if r.record.Session != "" && r.record.Session != session {
				return placementStoreError(errPlacementState)
			}
			state.Records[at].Session, state.Records[at].Pending = session, false
		}
		if err := r.store.write(owned, "placements.json", state); err != nil {
			return err
		}
		if !refused {
			r.record.Session, r.record.Pending = session, false
		}
		return nil
	})
}

// This positive negative witness is created ONLY by the canonical locked
// room check before any placements.json publication was attempted. A public
// no_room code, foreign RPC result or interrupted write cannot construct it.
type placementReservationRefusalV1 struct {
	actual    error
	store     *placementStore
	candidate *autoPlacementCandidate
}

func (failure *placementReservationRefusalV1) Error() string { return failure.actual.Error() }
func (failure *placementReservationRefusalV1) Unwrap() error { return failure.actual }
