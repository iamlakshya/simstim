package diag

import (
	"errors"
	"strings"
)

// The categories of records. A user turns them on by name, with
// `--debug=<categories>` or `debug start --only <categories>`.
const (
	CatAttach   = "attach"   // keys, output, and the holder's lock
	CatNet      = "net"      // pings, drops and reconnects, the shared connection, the bridge's requests
	CatMemory   = "memory"   // samples of the session's memory and pressure, and each kill
	CatLauncher = "launcher" // each frame of the launcher
	CatRuntime  = "runtime"  // Go's runtime metrics, the flight recorder, and CPU and heap profiles
)

// Cats is a set of categories.
type Cats uint8

// catNames are the categories in the order that help and records list them.
var catNames = []string{CatAttach, CatNet, CatMemory, CatLauncher, CatRuntime}

// AllCats is every category, and HostCats the ones a holder records.
// BridgeCats are the ones a bridge records.
var (
	AllCats    = CatsOf(catNames)
	HostCats   = CatsOf([]string{CatAttach, CatMemory, CatRuntime})
	BridgeCats = CatsOf([]string{CatNet, CatRuntime})
)

// CatList is every category's name, separated by commas: the value that
// `--debug` takes when it has none.
var CatList = strings.Join(catNames, ",")

func catBit(name string) Cats {
	for i, n := range catNames {
		if n == name {
			return 1 << i
		}
	}
	return 0
}

// CatsOf is the set of the categories named. It ignores a name it does not
// know, as a newer peer may send one.
func CatsOf(names []string) Cats {
	var c Cats
	for _, n := range names {
		c |= catBit(n)
	}
	return c
}

// ErrNoCats is the error of a list that names no category.
var ErrNoCats = errors.New("name at least one category: " + strings.Join(catNames, ", "))

// ParseCats reads a list of categories separated by commas, such as
// "attach,net". It fails on a name it does not know, and on an empty list.
func ParseCats(s string) (Cats, error) {
	var c Cats
	for _, n := range strings.Split(s, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		b := catBit(n)
		if b == 0 {
			return 0, errors.New("no category " + n + ": the categories are " + strings.Join(catNames, ", "))
		}
		c |= b
	}
	if c == 0 {
		return 0, ErrNoCats
	}
	return c, nil
}

// Has reports whether the set holds the category name.
func (c Cats) Has(name string) bool { return c&catBit(name) != 0 }

// List is the names in the set, in the usual order.
func (c Cats) List() []string {
	var out []string
	for i, n := range catNames {
		if c&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// String is the names in the set, separated by commas.
func (c Cats) String() string { return strings.Join(c.List(), ",") }

// keeps reports whether a file with this set of categories writes r. A
// record about debug itself, or with no category, always goes in.
func (c Cats) keeps(r *Record) bool {
	switch r.Ev {
	case EvStart, EvStop, EvSave, EvDropped:
		return true
	}
	return r.Cat == "" || c.Has(r.Cat)
}
