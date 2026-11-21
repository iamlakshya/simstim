package api

import (
	"time"

	"github.com/iamlakshya/simstim/internal/nettarget"
)

// NetworkHoldV1 is the version of display-only pending network records.
const NetworkHoldV1 = 1

// NetworkHoldUploadBudget is a display reason, never a byte allowance.
const NetworkHoldUploadBudget = "upload_budget"

// ValidNetworkHoldReason checks display metadata, never an answer authority.
func ValidNetworkHoldReason(reason string) bool {
	return reason == "" || reason == NetworkHoldUploadBudget
}

// NetworkHoldCapacity is the V1 display envelope's existing flow-sized limit.
// It neither grants a request nor replaces the producer's resource admission.
const NetworkHoldCapacity = 128

// NetworkHoldWaiterLimit covers the existing carrier and HTTP request limits.
const NetworkHoldWaiterLimit = 2 * NetworkHoldCapacity

// NetworkHoldStatus describes whether an existing held request is still open.
type NetworkHoldStatus string

const (
	// NetworkHoldOpen is a held request which can still receive an answer.
	NetworkHoldOpen NetworkHoldStatus = "open"
	// NetworkHoldClosed is a notification that an existing request ended.
	NetworkHoldClosed NetworkHoldStatus = "closed"
)

// PendingNetworkRequest is display data from one trusted egress lifetime.
// It never grants a target or supplies an approval, claim or secret authority.
// The original held request stays private to the egress process that made ID.
type PendingNetworkRequest struct {
	Version  int               `json:"v"`
	ID       string            `json:"id"`
	Revision uint64            `json:"revision"`
	Host     string            `json:"host"`
	Reason   string            `json:"reason,omitempty"`
	Redacted bool              `json:"redacted,omitempty"`
	Port     int               `json:"port"`
	Opened   time.Time         `json:"opened"`
	Expires  time.Time         `json:"expires"`
	Waiting  uint32            `json:"waiting"`
	Status   NetworkHoldStatus `json:"status"`
}

// NetworkHoldSnapshot lists the open records of one existing egress lifetime.
type NetworkHoldSnapshot struct {
	Version  int                     `json:"v"`
	Revision uint64                  `json:"revision"`
	Stopped  bool                    `json:"stopped,omitempty"`
	Requests []PendingNetworkRequest `json:"requests"`
}

// ValidNetworkHoldID checks an opaque 128-bit identifier, never host authority.
func ValidNetworkHoldID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := range len(id) {
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
			return false
		}
	}
	return true
}

// ValidPendingNetworkRequest checks the finite display envelope. The actual
// active-flow count and generation lifetime remain with the trusted producer.
func ValidPendingNetworkRequest(request PendingNetworkRequest) bool {
	host, err := nettarget.NormaliseHost(request.Host)
	validHost := err == nil && host == request.Host
	if request.Redacted {
		validHost = request.Host != "" && len(request.Host) <= 253 && CleanText(request.Host, 253) == request.Host
	}
	return ValidNetworkHoldReason(request.Reason) && validHost && request.Version == NetworkHoldV1 && ValidNetworkHoldID(request.ID) &&
		request.Revision > 0 &&
		request.Waiting <= NetworkHoldWaiterLimit &&
		request.Port >= 1 && request.Port <= 65535 && !request.Opened.IsZero() && request.Expires.After(request.Opened) &&
		request.Opened.Year() >= 0 && request.Opened.Year() <= 9999 && request.Expires.Year() >= 0 && request.Expires.Year() <= 9999 &&
		(request.Status == NetworkHoldOpen || request.Status == NetworkHoldClosed && request.Waiting == 0)
}

// ValidNetworkHoldSnapshot checks a complete display snapshot. Public masked
// host/port pairs are not identities; two private targets may share them.
func ValidNetworkHoldSnapshot(snapshot NetworkHoldSnapshot) bool {
	if snapshot.Version != NetworkHoldV1 || snapshot.Revision == 0 || len(snapshot.Requests) > NetworkHoldCapacity {
		return false
	}
	seen := make(map[string]bool, len(snapshot.Requests))
	for _, request := range snapshot.Requests {
		if !ValidPendingNetworkRequest(request) || request.Status != NetworkHoldOpen || request.Revision > snapshot.Revision || seen[request.ID] {
			return false
		}
		seen[request.ID] = true
	}
	return true
}
