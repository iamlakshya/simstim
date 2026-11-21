package guest

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (g *Init) prepareSession(a *proto.CtlStartArgs) error {
	if g.unwalled != nil {
		return g.projectUnwalledV1(a, true)
	}
	if err := g.validateBootPackageSettings(a.PackageManagers); err != nil {
		return err
	}
	if err := g.validatePackageStateFiles(a.PackageManagers, a.StateFiles); err != nil {
		return err
	}
	env, err := PackageManagerEnvironment(a.PackageManagers, a.Env)
	if err != nil {
		return err
	}
	a.Env = env
	if g.vm == nil {
		if err := paths.EnsurePrivateDir(g.path(host.GuestRunDir())); err != nil {
			return err
		}
	} else if err := g.vm.validateSession(*a); err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(g.path(host.GuestHome)); err != nil {
		return err
	}
	if err := g.setupDNS(a.DNS); err != nil {
		return err
	}
	if err := g.installSessionTrust(a); err != nil {
		return err
	}
	if err := g.restoreState(*a); err != nil {
		return err
	}
	if err := g.applyBootDecoys(g.lifetime, a.Decoys); err != nil {
		return err
	}
	if err := g.applyBootPackageSettings(g.lifetime, a.PackageManagers); err != nil {
		return err
	}
	if err := g.memberProxyEnv(a, true); err != nil {
		return err
	}
	if err := g.memberSSHAgentEnv(a); err != nil {
		return err
	}
	g.mu.Lock()
	g.execEnv = append([]string(nil), a.Env...)
	if g.vm != nil {
		g.execEnv = g.vm.childEnvironment(a.Env)
	}
	g.execCwd = a.Cwd
	g.sessionPrepared = true
	g.mu.Unlock()
	return nil
}

func decodeControllerPrepare(raw []byte) (proto.CtlPrepareArgs, error) {
	var args proto.CtlPrepareArgs
	fields, err := api.JSONObjectFields(raw, "version", "start")
	if err != nil || len(fields) != 2 || len(fields["version"]) == 0 || len(fields["start"]) == 0 {
		return args, execError("invalid controller preparation")
	}
	names := map[string]bool{}
	startType := reflect.TypeFor[proto.CtlStartArgs]()
	keys := make([]string, 0, startType.NumField())
	for index := range startType.NumField() {
		name := strings.Split(startType.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			names[name] = true
			keys = append(keys, name)
		}
	}
	start, err := api.JSONObjectFields(fields["start"], keys...)
	if err != nil {
		return args, execError("invalid controller preparation fields")
	}
	for name := range start {
		if !names[name] {
			return args, execError("unsupported controller preparation field")
		}
	}
	if _, exists := start["prepared_claim"]; exists {
		return args, execError("controller preparation cannot carry a member claim")
	}
	if _, exists := start["secret_modes"]; exists {
		return args, execError("controller preparation cannot install secret delivery modes")
	}
	if json.Unmarshal(raw, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Start.Session) || args.Start.PendingMembers <= 0 || args.Start.RunToken != "" || len(args.Start.Argv) != 0 || len(args.Start.Hooks) != 0 || len(args.Start.Secrets) != 0 {
		return args, execError("controller preparation cannot launch or expose an agent")
	}
	if err := validatePackageManagerWire(fields["start"], args.Start); err != nil {
		return args, err
	}
	return args, nil
}

func (g *Init) prepareController(raw []byte) (proto.CtlPrepareResult, error) {
	args, err := decodeControllerPrepare(raw)
	if err != nil {
		return proto.CtlPrepareResult{}, err
	}
	if g.vm != nil {
		if err := g.vm.validateSession(args.Start); err != nil {
			return proto.CtlPrepareResult{}, err
		}
	}
	g.mu.Lock()
	if g.ended || g.starting || g.sessionPrepared || g.holder != nil {
		g.mu.Unlock()
		return proto.CtlPrepareResult{}, execError("the session infrastructure already started")
	}
	g.starting = true
	g.primary, g.pendingBoot = args.Start.Session, args.Start.PendingMembers
	g.bootAdmission = true
	g.mu.Unlock()
	err = g.prepareSession(&args.Start)
	g.mu.Lock()
	g.starting = false
	g.mu.Unlock()
	if err != nil {
		go g.end()
		return proto.CtlPrepareResult{}, err
	}
	return proto.CtlPrepareResult{Version: 1, GuestABI: g.ptyABI()}, nil
}
