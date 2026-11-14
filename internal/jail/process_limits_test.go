package jail

import "testing"

func TestLandlockModeRefusesOtherProcessesResourceChanges(t *testing.T) {
	for _, profile := range []Profile{ProfileVMM, ProfileConverter, ProfileGit, ProfileEgress} {
		for _, landlockMode := range []bool{false, true} {
			progs, err := filters(profile, NetHost, landlockMode)
			if err != nil {
				t.Fatal(err)
			}
			other := uint32(retAllow)
			if landlockMode {
				other = eperm
			}
			for _, name := range []string{"prlimit64", "sched_setscheduler", "sched_setattr", "sched_setparam", "sched_setaffinity"} {
				for _, target := range []uint64{0, 37, 1 << 32, ^uint64(0)} {
					want := other
					if target == 0 {
						want = retAllow
					}
					if got := verdict(t, progs, call{name: name, args: [6]uint64{target}}); got != want {
						t.Errorf("%s mode=%v %s target=%d: %#x, want %#x", profile, landlockMode, name, target, got, want)
					}
				}
			}
			for _, c := range []struct {
				name       string
				which, who uint64
				self       bool
			}{
				{"setpriority", 0, 0, true}, {"setpriority", 0, 37, false},
				{"setpriority", 1, 0, false}, {"setpriority", 2, 0, false},
				{"setpriority", 1 << 32, 0, false}, {"setpriority", 0, 1 << 32, false},
				{"setpriority", 1<<32 | 1, 0, false}, {"setpriority", 1<<32 | 2, 0, false},
				{"ioprio_set", 1, 0, true}, {"ioprio_set", 1, 37, false},
				{"ioprio_set", 0, 0, false}, {"ioprio_set", 2, 0, false}, {"ioprio_set", 3, 0, false},
				{"ioprio_set", 1<<32 | 1, 0, false}, {"ioprio_set", 1, 1 << 32, false},
			} {
				want := other
				if c.self {
					want = retAllow
				}
				if got := verdict(t, progs, call{name: c.name, args: [6]uint64{c.which, c.who}}); got != want {
					t.Errorf("%s mode=%v %s(%d,%d): %#x, want %#x", profile, landlockMode, c.name, c.which, c.who, got, want)
				}
			}
		}
	}
}
