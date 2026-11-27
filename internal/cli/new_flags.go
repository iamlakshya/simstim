package cli

import "github.com/spf13/pflag"

// addOldNewFlags adds the hidden flags that keep the old forms of new while
// their rows last: -d for --dir, and --setup and --no-setup for
// --setup-script and --no-setup-script (cli-spec 3.2).
func addOldNewFlags(f *pflag.FlagSet, dir *string, setup, noSetup *bool) {
	if liveOld("new -d") {
		f.StringVarP(dir, "d", "d", "~", "")
		hideOld(f, "d", "new -d")
	}
	for _, o := range []struct {
		name string
		p    *bool
	}{{"setup", setup}, {"no-setup", noSetup}} {
		if id := "new --" + o.name; liveOld(id) {
			f.BoolVar(o.p, o.name, false, "")
			hideOld(f, o.name, id)
		}
	}
}
