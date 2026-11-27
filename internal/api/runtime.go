package api

import (
	"time"
)

// RuntimeFile describes presence of an entry named by the requesting client.
// It is an inventory claim, not proof of a working sandbox driver.
type RuntimeFile struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Present bool   `json:"present"`
}

// RuntimeTransfer reports progress of a transfer owned by this connection.
type RuntimeTransfer struct {
	SHA256   string    `json:"sha256"`
	Received int64     `json:"received"`
	Started  time.Time `json:"started"`
}
