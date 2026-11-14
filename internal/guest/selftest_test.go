package guest

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSelfTestOKPowersOff(t *testing.T) {
	var poweredOff bool
	env := selfTestEnv{
		getpid:      func() int { return 1 },
		prepareFS:   func() error { return nil },
		rootDev:     func() (uint32, uint32, error) { return 253, 0, nil },
		dm0BlockDev: func() (uint32, uint32, error) { return 253, 0, nil },
		sysDM0Dev:   func() (string, error) { return "253:0", nil },
		powerOff:    func() error { poweredOff = true; return nil },
	}
	var out, errOut bytes.Buffer
	if err := runSelfTestWith(env, &out, &errOut); err != nil {
		t.Fatalf("runSelfTestWith: %v", err)
	}
	if out.String() != "self-test ok\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "self-test ok\n")
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
	if !poweredOff {
		t.Error("expected powerOff to be called")
	}
}

func TestSelfTestRefusesNonPID1WithoutPowerOff(t *testing.T) {
	var poweredOff bool
	env := defaultSelfTestEnv()
	env.getpid = func() int { return 42 }
	env.powerOff = func() error { poweredOff = true; return nil }

	var out, errOut bytes.Buffer
	err := runSelfTestWith(env, &out, &errOut)
	var pe *proto.Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "PID 1") || pe.Hint == "" {
		t.Fatalf("expected proto.Error with Hint, got %#v", err)
	}
	if poweredOff {
		t.Error("non-PID-1 self-test must not power off the machine")
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

func TestSelfTestRefusesWrongRootDevice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*selfTestEnv)
		want   string
	}{
		{
			name:   "mount failed",
			change: func(e *selfTestEnv) { e.prepareFS = func() error { return errors.New("mount /proc: EPERM") } },
			want:   "mount /proc",
		},
		{
			name:   "different block device",
			change: func(e *selfTestEnv) { e.rootDev = func() (uint32, uint32, error) { return 254, 0, nil } },
			want:   "not /dev/dm-0",
		},
		{
			name: "missing dm-0",
			change: func(e *selfTestEnv) {
				e.dm0BlockDev = func() (uint32, uint32, error) { return 0, 0, errors.New("ENOENT") }
			},
			want: "stat /dev/dm-0",
		},
		{
			name:   "sysfs mismatch",
			change: func(e *selfTestEnv) { e.sysDM0Dev = func() (string, error) { return "253:1", nil } },
			want:   "/sys/block/dm-0/dev",
		},
	} {
		var poweredOff bool
		env := selfTestEnv{
			getpid:      func() int { return 1 },
			prepareFS:   func() error { return nil },
			rootDev:     func() (uint32, uint32, error) { return 253, 0, nil },
			dm0BlockDev: func() (uint32, uint32, error) { return 253, 0, nil },
			sysDM0Dev:   func() (string, error) { return "253:0", nil },
			powerOff:    func() error { poweredOff = true; return nil },
		}
		tc.change(&env)
		var out, errOut bytes.Buffer
		err := runSelfTestWith(env, &out, &errOut)
		var pe *proto.Error
		if !errors.As(err, &pe) || !strings.Contains(pe.Message, tc.want) || pe.Hint == "" {
			t.Errorf("%s: got %#v, want %q", tc.name, err, tc.want)
		}
		if strings.Contains(out.String(), "self-test ok") {
			t.Errorf("%s: printed self-test ok on failure", tc.name)
		}
		if !poweredOff {
			t.Errorf("%s: PID 1 failure should power off cleanly instead of panicking init", tc.name)
		}
	}
}

func TestRunRejectsUnknownArgsAndNonPID1SelfTest(t *testing.T) {
	var pe *proto.Error
	if err := Run("--unknown"); !errors.As(err, &pe) || pe.Hint == "" {
		t.Errorf("Run(--unknown) = %#v", err)
	}
	// In unit tests os.Getpid() != 1, so Run("--self-test") exercises defaultSelfTestEnv safely.
	if err := Run("--self-test"); !errors.As(err, &pe) || !strings.Contains(pe.Message, "PID 1") {
		t.Errorf("Run(--self-test) = %#v", err)
	}
}
