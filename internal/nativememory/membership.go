package nativememory

import "strings"

// MembershipRecord is one kernel id:controllers:path row, not scope authority.
type MembershipRecord struct {
	Hierarchy   string
	Controllers []string
	Path        string
}

// Membership retains the existing path lexer's valid rows and also records
// malformed input for strict native admission. Peer classification can retain
// its prior conservative policy without a separate parser.
type Membership struct {
	Records   []MembershipRecord
	Malformed bool
}

// ParseMembership is the one lexer extracted from the existing peer reader.
// Rows with an absolute path remain visible to legacy peer classification;
// strict accounting additionally refuses Malformed and unsupported modes.
func ParseMembership(input []byte) Membership {
	var out Membership
	for _, line := range strings.Split(string(input), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, rest, found := strings.Cut(line, ":")
		if !found {
			out.Malformed = true
			continue
		}
		controllers, path, found := strings.Cut(rest, ":")
		if !found || !strings.HasPrefix(path, "/") {
			out.Malformed = true
			continue
		}
		row := MembershipRecord{Hierarchy: id, Path: path}
		if controllers != "" {
			row.Controllers = strings.Split(controllers, ",")
			for _, controller := range row.Controllers {
				if controller == "" {
					out.Malformed = true
				}
			}
		}
		if _, err := decimal(id); err != nil || id == "0" && controllers != "" {
			out.Malformed = true
		}
		out.Records = append(out.Records, row)
	}
	return out
}
