//go:build linux

package guest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	vmDockerGateway = "172.30.0.1"
	vmDockerPool    = "172.30.0.0/16"
)

func vmDockerArgs(socket, dataRoot, execRoot string) []string {
	return []string{"--host=unix://" + socket, "--group=docker", "--data-root=" + dataRoot, "--exec-root=" + execRoot, "--pidfile=" + filepath.Join(execRoot, "dockerd.pid"), "--exec-opt=native.cgroupdriver=cgroupfs", "--bip=" + vmDockerGateway + "/24", "--default-address-pool=base=" + vmDockerPool + ",size=24", "--dns=" + vmDockerGateway, "--ipv6=false", "--shutdown-timeout=3"}
}

func (v *vmRuntime) startDocker() error {
	root, err := os.OpenRoot("/")
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{".persist/docker", "run/" + brand.Name + "/docker-exec"} {
		if err := vmOwnedDirectory(root, name, 0o700, 0, 0, v.files.ops.own); err != nil {
			return err
		}
	}
	path, code, err := guestExecutable("dockerd", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, "/")
	if err != nil {
		return &proto.Error{Code: "docker_missing", Message: fmt.Sprintf("docker=true requires the image's actual dockerd executable (status%d)", code), Hint: "Choose a microvm image containing dockerd and the Docker client, or turn sandbox.docker off."}
	}
	if _, _, err := guestExecutable("docker", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, "/"); err != nil {
		return &proto.Error{Code: "docker_missing", Message: "docker=true requires the image's actual Docker client", Hint: "Choose a microvm image containing both dockerd and docker, or turn sandbox.docker off."}
	}
	socket := filepath.Join("/run", brand.Name, "docker.sock")
	execRoot := filepath.Join("/run", brand.Name, "docker-exec")
	args := append([]string{path}, vmDockerArgs(socket, "/.persist/docker", execRoot)...)
	run := newOwnedCommand(v.ctx, proto.CtlExecArgs{ID: "dockerd", Argv: args, Cwd: "/"}, nil)
	run.vmRole = vmDockerCommand
	command := exec.Command(path, args[1:]...)
	command.SysProcAttr = vmDockerProcess()
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C", "HTTP_PROXY=http://127.0.0.1:3128", "HTTPS_PROXY=http://127.0.0.1:3128", "NO_PROXY=localhost,127.0.0.1,::1", "SSL_CERT_FILE=" + filepath.Join("/run", brand.Name, "ca-bundle.pem")}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := v.init.startOwned(run, command); err != nil {
		run.cancel()
		return err
	}
	v.docker = run
	go func() {
		_, err := v.init.waitOwned(run)
		if err != nil && v.ctx.Err() == nil {
			v.setError(fmt.Errorf("the owned guest Docker daemon exited: %w", err))
			go v.init.end()
		}
	}()
	ctx, cancel := context.WithTimeout(v.ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := vmDockerPing(ctx, socket); err == nil {
			info, err := os.Lstat(socket)
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != 0 || stat.Gid != dockerGID || info.Mode().Perm()&0o060 != 0o060 || info.Mode().Perm()&0o007 != 0 {
				return vmError("the guest Docker socket has unsafe account or group permissions")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			run.cancel()
			_ = run.signal(9)
			return vmError("the image's Docker daemon did not become ready")
		case <-run.procDone:
			return vmError("the image's Docker daemon exited before readiness")
		case <-ticker.C:
		}
	}
}

func vmDockerPing(ctx context.Context, socket string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("Docker readiness redirects are refused")
	}}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 3))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || string(data) != "OK" {
		return fmt.Errorf("guest Docker readiness failed")
	}
	return nil
}

func (v *vmRuntime) stopDocker() error {
	if v.docker == nil {
		return nil
	}
	run := v.docker
	_ = run.signal(uint32(syscall.SIGTERM))
	select {
	case <-run.procDone:
		return nil
	case <-time.After(4 * time.Second):
		run.cancel()
		_ = v.memory.killMember(run.vmGroup)
		_ = run.signal(uint32(syscall.SIGKILL))
		<-run.procDone
		return vmError("the owned Docker daemon did not finish its declared shutdown deadline")
	}
}
