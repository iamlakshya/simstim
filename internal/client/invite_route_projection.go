package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type explicitRouteCredentialV1 struct {
	path       string
	file       *os.File
	info       os.FileInfo
	missing    bool
	socket     bool
	parent     *os.Root
	parentInfo os.FileInfo
}
type explicitBastionV1 struct {
	resolved explicitRouteHopV1
	keys     []hostKey
	alias    string
	pinPath  string
}
type routeProjectionFileV1 struct {
	path          string
	info          os.FileInfo
	data          []byte
	directoryInfo os.FileInfo
}
type explicitRouteProjectionV1 struct {
	source        *explicitRouteSourceV1
	bastions      []explicitBastionV1
	credentials   []*explicitRouteCredentialV1
	directory     string
	directoryInfo os.FileInfo
	root          *os.Root
	files         []*routeProjectionFileV1
	closed        bool
}

// captureExplicitBastionsV1 consumes only the original preflighted route source.
// Native OpenSSH decides existing host trust; no new/yes bypass is accepted.
func captureExplicitBastionsV1(ctx context.Context, source *explicitRouteSourceV1) (result *explicitRouteProjectionV1, failure error) {
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "joined-bastions-")
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	projection := &explicitRouteProjectionV1{source: source, directory: directory, directoryInfo: info, root: root}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, projection.close())
		}
	}()
	for index, hop := range source.hops {
		if err = projection.captureCredentials(hop); err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(source.logical + "/" + strconv.Itoa(index)))
		host := Host{Name: "join-route-" + hex.EncodeToString(sum[:12]), SSH: fmt.Sprintf("bastion-%d", index+1)}
		config, err := projection.render(&hop, index)
		if err != nil {
			return nil, err
		}
		file, err := projection.writeFile("probe-"+strconv.Itoa(index)+".conf", []byte(config))
		if err != nil {
			return nil, err
		}
		host.SSHConfig = file.path
		native, err := (&SSH{Host: host}).WithNativeSource(&joinedBastionProjectionSourceV1{projection: projection, file: file, route: cloneObservationHost(host)})
		if err != nil {
			return nil, err
		}
		owned, finish, err := native.beginNativeSource(ctx)
		if err != nil {
			return nil, err
		}
		verdict, trustErr := trustHostKey(owned, host, "")
		var key hostKey
		if trustErr == nil {
			if verdict.kind != trusted {
				trustErr = routeSourceErrorV1("an existing bastion key is not already trusted by the selected user source")
			} else {
				key, trustErr = fetchTrusted(owned, host, verdict)
			}
		}
		trustErr = errors.Join(trustErr, finish())
		if trustErr != nil {
			return nil, trustErr
		}
		pin, err := projection.writeFile("bastion-"+strconv.Itoa(index+1)+".keys", []byte(key.line(host.SSH)+"\n"))
		if err != nil {
			return nil, err
		}
		projection.bastions = append(projection.bastions, explicitBastionV1{resolved: hop, keys: []hostKey{key}, alias: host.SSH, pinPath: pin.path})
	}
	if err = projection.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return projection, nil
}
func (p *explicitRouteProjectionV1) captureCredentials(hop explicitRouteHopV1) error {
	for _, keyword := range []string{"userknownhostsfile", "globalknownhostsfile"} {
		var paths []string
		for _, raw := range hop.values[keyword] {
			_, values, err := routeConfigLineV1(keyword + " " + raw)
			if err != nil {
				return err
			}
			paths = append(paths, values...)
		}
		hop.values[keyword] = paths
	}

	for _, keyword := range []string{"identityfile", "certificatefile", "userknownhostsfile", "globalknownhostsfile"} {
		for _, value := range hop.values[keyword] {
			if value == "none" || value == "" || value == os.DevNull {
				continue
			}
			if strings.ContainsAny(value, "%$\x00\r\n") {
				return routeSourceErrorV1("a user-owned SSH credential/trust reference has unsupported dynamic expansion")
			}
			path := expand(value)
			if !filepath.IsAbs(path) {
				absolute, err := filepath.Abs(path)
				if err != nil {
					return err
				}
				path = absolute
			}
			duplicate := false
			for _, original := range p.credentials {
				if original.path == path {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			before, err := os.Lstat(path)
			if os.IsNotExist(err) {
				p.credentials = append(p.credentials, &explicitRouteCredentialV1{path: path, missing: true})
				continue
			}
			if err != nil {
				return err
			}
			if !before.Mode().IsRegular() {
				return routeSourceErrorV1("an SSH identity/trust reference is linked or non-regular")
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			opened, err := file.Stat()
			if err != nil || !os.SameFile(before, opened) {
				file.Close()
				return routeSourceErrorV1("a user-owned SSH reference changed while opening")
			}
			if err = routeConfigNativeSafeV1(file, opened); err != nil {
				file.Close()
				return err
			}
			p.credentials = append(p.credentials, &explicitRouteCredentialV1{path: path, file: file, info: opened})
		}
	}
	if len(p.credentials) > 128 {
		return routeSourceErrorV1("the original credential reference count exceeds its bound")
	}
	agent := firstValueV1(hop.values, "identityagent")
	if agent == "" || agent == "SSH_AUTH_SOCK" {
		agent = os.Getenv("SSH_AUTH_SOCK")
	}
	if agent == "" || agent == "none" {
		hop.values["identityagent"] = []string{"none"}
		return nil
	}
	if strings.ContainsAny(agent, "%$\x00\r\n") {
		return routeSourceErrorV1("an SSH agent reference has unsupported dynamic expansion")
	}
	agent = expand(agent)
	if !filepath.IsAbs(agent) {
		return routeSourceErrorV1("the original SSH agent reference is not absolute")
	}
	credential, err := openRouteSocketV1(agent, "")
	if err != nil {
		return err
	}
	p.credentials = append(p.credentials, credential)
	if len(p.credentials) > 128 {
		return routeSourceErrorV1("the original credential reference count exceeds its bound")
	}
	hop.values["identityagent"] = []string{agent}
	return nil
}
func (p *explicitRouteProjectionV1) render(current *explicitRouteHopV1, index int) (string, error) {
	if len(p.credentials) > 128 {
		return "", routeSourceErrorV1("the original credential reference count exceeds its bound")
	}
	var text strings.Builder
	for _, option := range noForwarding {
		fmt.Fprintf(&text, "%s %s\n", option[0], option[1])
	}
	text.WriteString("ClearAllForwardings yes\nPermitLocalCommand no\nCanonicalizeHostname no\nBatchMode yes\n")
	for ordinal, bastion := range p.bastions {
		if err := renderBastionHostV1(&text, bastion.alias, bastion.resolved, bastion.keys, bastion.pinPath, ordinal, p.bastions); err != nil {
			return "", err
		}
	}
	if current != nil {
		if err := renderBastionHostV1(&text, fmt.Sprintf("bastion-%d", index+1), *current, nil, "", index, p.bastions); err != nil {
			return "", err
		}
	}
	return text.String(), nil
}
func renderBastionHostV1(text *strings.Builder, alias string, hop explicitRouteHopV1, keys []hostKey, pinPath string, index int, earlier []explicitBastionV1) error {
	fmt.Fprintf(text, "Host %s\n    HostName %s\n    User %s\n    Port %d\n    IdentitiesOnly yes\n    PreferredAuthentications publickey\n    PasswordAuthentication no\n    KbdInteractiveAuthentication no\n", alias, hop.hostname, hop.user, hop.port)
	for _, keyword := range []string{"identityfile", "certificatefile"} {
		for _, value := range hop.values[keyword] {
			if value != "none" {
				fmt.Fprintf(text, "    %s %q\n", keyword, expand(value))
			}
		}
	}
	agent := firstValueV1(hop.values, "identityagent")
	if agent != "" {
		fmt.Fprintf(text, "    IdentityAgent %q\n", agent)
	}
	hostAlias := firstValueV1(hop.values, "hostkeyalias")
	if len(keys) > 0 {
		hostAlias = alias
	} else if hostAlias == "" {
		hostAlias = hop.hostname
	}
	fmt.Fprintf(text, "    HostKeyAlias %q\n    StrictHostKeyChecking yes\n    UpdateHostKeys no\n    VerifyHostKeyDNS no\n    NoHostAuthenticationForLocalhost no\n    KnownHostsCommand none\n", hostAlias)
	if len(keys) == 0 {
		for _, keyword := range []string{"userknownhostsfile", "globalknownhostsfile"} {
			values := hop.values[keyword]
			if len(values) == 0 {
				continue
			}
			fmt.Fprintf(text, "    %s", keyword)
			for _, value := range values {
				fmt.Fprintf(text, " %q", expand(value))
			}
			text.WriteByte('\n')
		}
	} else {
		if pinPath == "" {
			return routeSourceErrorV1("the original bastion public-key pin is missing")
		}
		fmt.Fprintf(text, "    UserKnownHostsFile %q\n    GlobalKnownHostsFile %s\n", pinPath, os.DevNull)
	}

	if index == 0 {
		text.WriteString("    ProxyJump none\n")
	} else {
		aliases := make([]string, index)
		for i := 0; i < index; i++ {
			aliases[i] = earlier[i].alias
		}
		fmt.Fprintf(text, "    ProxyJump %s\n", strings.Join(aliases, ","))
	}
	text.WriteString("    ProxyCommand none\n\n")
	return nil
}
func (p *explicitRouteProjectionV1) writeFile(name string, data []byte) (*routeProjectionFileV1, error) {
	path := filepath.Join(p.directory, name)
	var info os.FileInfo
	err := writeAtomicFileWithHooks(path, ".route-projection-*", data, atomicFileHooks{beforeReplace: func(root *os.Root, base string) error {
		if _, err := root.Lstat(base); !os.IsNotExist(err) {
			return routeSourceErrorV1("the new route projection destination was filled")
		}
		here, err := root.Stat(".")
		if err != nil || !os.SameFile(here, p.directoryInfo) {
			return routeSourceErrorV1("the original route projection root changed")
		}
		return nil
	}, published: func(published os.FileInfo) { info = published }})
	if err != nil {
		return nil, err
	}
	file := &routeProjectionFileV1{path: path, info: info, data: append([]byte(nil), data...), directoryInfo: p.directoryInfo}
	p.files = append(p.files, file)
	return file, nil
}
func (p *explicitRouteProjectionV1) check(ctx context.Context) error {
	if p == nil || p.closed {
		return routeSourceErrorV1("the original bastion projection ended")
	}
	if err := p.source.check(ctx); err != nil {
		return err
	}
	directory, err := os.Lstat(p.directory)
	if err != nil || !os.SameFile(directory, p.directoryInfo) {
		return routeSourceErrorV1("the original bastion projection directory changed")
	}
	for _, credential := range p.credentials {
		if credential.socket {
			if err := credential.checkSocket(); err != nil {
				return err
			}
			continue
		}
		current, err := os.Lstat(credential.path)
		if credential.missing {
			if !os.IsNotExist(err) {
				return routeSourceErrorV1("a previously absent SSH credential reference appeared")
			}
			continue
		}
		if err != nil {
			return err
		}
		opened, err := credential.file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(current, credential.info) || !os.SameFile(opened, credential.info) || opened.Size() != credential.info.Size() || !opened.ModTime().Equal(credential.info.ModTime()) {
			return routeSourceErrorV1("a user-owned SSH credential/trust reference changed")
		}
		if err = routeConfigNativeSafeV1(credential.file, opened); err != nil {
			return err
		}
	}
	for _, file := range p.files {
		current, err := os.Lstat(file.path)
		if err != nil || !os.SameFile(current, file.info) {
			return routeSourceErrorV1("an original bastion projection file changed")
		}
		data, err := os.ReadFile(file.path)
		if err != nil {
			return err
		}
		if string(data) != string(file.data) {
			return routeSourceErrorV1("an original bastion projection was edited")
		}
	}
	return ctx.Err()
}
func (p *explicitRouteProjectionV1) close() error {
	if p == nil || p.closed {
		return nil
	}
	here, err := os.Lstat(p.directory)
	if err != nil || !os.SameFile(here, p.directoryInfo) {
		return routeSourceErrorV1("the original projection root changed before cleanup")
	}
	var failure error
	for _, credential := range p.credentials {
		if credential.parent != nil {
			failure = errors.Join(failure, credential.parent.Close())
			credential.parent = nil
		}
		if credential.file != nil {
			failure = errors.Join(failure, credential.file.Close())
			credential.file = nil
		}
	}
	for _, file := range p.files {
		if file.info == nil {
			continue
		}
		current, err := p.root.Lstat(filepath.Base(file.path))
		if err != nil || !os.SameFile(current, file.info) {
			failure = errors.Join(failure, routeSourceErrorV1("an original projection was replaced before cleanup"))
			continue
		}
		data, err := p.root.ReadFile(filepath.Base(file.path))
		if err != nil || string(data) != string(file.data) {
			failure = errors.Join(failure, routeSourceErrorV1("an original projection was edited before cleanup"))
			continue
		}
		if err = p.root.Remove(filepath.Base(file.path)); err != nil {
			failure = errors.Join(failure, err)
		} else {
			file.info = nil
		}
	}
	if failure == nil {
		failure = os.Remove(p.directory)
	}
	if failure == nil {
		failure = p.root.Close()
	}
	if failure == nil {
		p.closed = true
	}
	return failure
}

type joinedBastionProjectionSourceV1 struct {
	projection *explicitRouteProjectionV1
	file       *routeProjectionFileV1
	route      Host
}

func (*joinedBastionProjectionSourceV1) joinedProjectionV1() {}
func (s *joinedBastionProjectionSourceV1) OpenSSHNativeSource(ctx context.Context) (SSHNativeSourceLease, error) {
	if s == nil || s.file == nil || s.route.SSHConfig != s.file.path {
		return nil, routeSourceErrorV1("the bastion projection has another route")
	}
	lease := &joinedBastionProjectionLeaseV1{source: s}
	if err := lease.Revalidate(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}

type joinedBastionProjectionLeaseV1 struct {
	source *joinedBastionProjectionSourceV1
	closed bool
}

func (s *joinedBastionProjectionLeaseV1) Revalidate(ctx context.Context) error {
	if s == nil || s.closed || s.source == nil {
		return routeSourceErrorV1("the original bastion projection lease ended")
	}
	return s.source.projection.check(ctx)
}
func (s *joinedBastionProjectionLeaseV1) Close() error {
	if s != nil {
		s.closed = true
	}
	return nil
}
