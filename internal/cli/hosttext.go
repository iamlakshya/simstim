package cli

import (
	"encoding/json"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// What a host sends is not trusted. A command prints the host's text
// through shown, which removes control and hidden characters, and checks a
// session ID before it prints it or puts it in a command for the user.

// checkSessionID fails when id, the ID of a session that host sent, is not
// one that a host makes: 1 to 32 lower-case base32 characters (a-z, 2-7).
// Such an ID is safe to print, and to put in a command line.
func checkSessionID(host, id string) error {
	if validSessionID(id) {
		return nil
	}
	return &client.UserError{Msg: fmt.Sprintf("%s sent a session ID that is not valid: %q", host, shown(id)),
		Hint: brand.Name + " host doctor " + shellWord(host), Code: "bad_session_id"}
}

func validSessionID(id string) bool {
	return api.ValidSessionID(id)
}

// validJSONData keeps the host's exact JSON bytes when they form one value.
func validJSONData(data json.RawMessage) json.RawMessage {
	if !json.Valid(data) {
		return nil
	}
	return data
}
