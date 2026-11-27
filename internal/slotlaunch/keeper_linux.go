//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/slotlease"
	"golang.org/x/sys/unix"
)

// Init runs before normal argument handling, and after jail.Init. Only the
// fixed executable's private, credential-dropped stages enter this path.
func Init() {
	if len(os.Args) != 2 || os.Args[0] != brand.Name+"-slot" {
		return
	}
	if err := secretmem.Protect(); err != nil {
		os.Exit(125)
	}
	accounts, err := slotAccounts()
	if err != nil {
		os.Exit(125)
	}
	valid := false
	for _, account := range accounts {
		if account.uid == os.Getuid() {
			valid = true
		}
	}
	if !valid || os.Geteuid() != os.Getuid() {
		os.Exit(125)
	}
	switch os.Args[1] {
	case "purge-v1":
		// kill(-1) as the already dropped uid cannot signal any other
		// account. The caller inspects /proc again before taking the slot.
		for range 4 {
			if err := unix.Kill(-1, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
				os.Exit(125)
			}
		}
		os.Exit(0)
	case "keeper-v1":
		if err := runKeeper(); err != nil {
			os.Exit(125)
		}
		os.Exit(0)
	default:
		os.Exit(125)
	}
}

func runKeeper() error {
	planFile := os.NewFile(3, "keeper-plan")
	reply := os.NewFile(4, "keeper-reply")
	lock := os.NewFile(5, "slot-lock")
	controlFile := os.NewFile(6, "root-control")
	defer planFile.Close()
	defer reply.Close()
	defer lock.Close()
	defer controlFile.Close()
	// These capabilities remain with the keeper; no VMM inherits them.
	for _, file := range []*os.File{planFile, reply, lock, controlFile} {
		unix.CloseOnExec(int(file.Fd()))
	}
	var plan keeperPlan
	dec := json.NewDecoder(io.LimitReader(planFile, RequestMax+1))
	dec.DisallowUnknownFields()
	if dec.Decode(&plan) != nil || dec.Decode(new(any)) != io.EOF || plan.Request.validate() != nil || plan.Slot < 0 || plan.Slot >= SlotCount || plan.SlotUID != os.Getuid() || plan.SlotGID != os.Getgid() || plan.GID <= 0 {
		return refused("the slot keeper received an invalid root allocation")
	}
	actual, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	member, err := unifiedPath(string(actual))
	if err != nil || member != plan.Cgroup.Path {
		return refused("sudo or the keeper left the original session cgroup")
	}
	lease, err := slotlease.CaptureKeeperV1()
	if err != nil {
		return err
	}
	defer lease.Close()
	cgroupFile := os.NewFile(12, "root-slot-cgroup")
	directory := os.NewFile(13, "root-vm-directory")
	disk := os.NewFile(14, "root-session-disk")
	defer cgroupFile.Close()
	defer directory.Close()
	defer disk.Close()
	for _, file := range []*os.File{cgroupFile, directory, disk} {
		unix.CloseOnExec(int(file.Fd()))
	}
	st, err := lock.Stat()
	if err != nil || !singleRegular(st, 0) || st.Mode().Perm() != 0o644 {
		return refused("the keeper has no protected slot lock")
	}
	var fdstat unix.Stat_t
	if err := unix.Fstat(int(lock.Fd()), &fdstat); err != nil {
		return err
	}
	var pathstat unix.Stat_t
	if err := unix.Stat(RootPath()+fmt.Sprintf("/slots/%02d.lock", plan.Slot), &pathstat); err != nil || fdstat.Ino != pathstat.Ino || fdstat.Dev != pathstat.Dev {
		return refused("the keeper slot lock differs from its root allocation")
	}
	listener, err := net.FileListener(controlFile)
	if err != nil {
		return err
	}
	defer listener.Close()
	runtimeFiles := make([]*os.File, 5)
	for i := range runtimeFiles {
		file := os.NewFile(uintptr(7+i), "runtime-"+strconv.Itoa(i))
		runtimeFiles[i] = file
		defer file.Close()
		st, err := file.Stat()
		if err != nil || !singleRegular(st, 0) || st.Mode().Perm()&0o222 != 0 {
			return refused("the keeper runtime vector contains a writable or unprotected file")
		}
		b := &backend{owner: 0}
		if err := b.checkStore(file, plan.Request.digests()[i], 0); err != nil {
			return err
		}
	}
	vmDir := RootPath() + "/vms/" + plan.Request.Session
	executedVMM, err := sealSlotVMM(runtimeFiles[0], plan.Request.VMM)
	if err != nil {
		return err
	}
	defer executedVMM.Close()
	runtimeFiles[0] = executedVMM
	if err := prepareKeeperDisk(plan, directory, disk); err != nil {
		return err
	}
	if err := unix.Umask(0o007); err < 0 {
		return refused("the keeper could not set its private socket umask")
	}
	args, configuration, err := fixedCommand(plan.Request, vmDir)
	if err != nil {
		return err
	}
	if len(configuration) > 0 {
		root, err := os.OpenRoot(vmDir)
		if err != nil {
			return err
		}
		file, err := root.OpenFile("firecracker.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o640)
		if err != nil {
			root.Close()
			return err
		}
		_, err = file.Write(configuration)
		err = joinClose(err, file)
		root.Close()
		if err != nil {
			return err
		}
	}
	grants := []jail.FileRuleV1{{FD: 3, Role: jail.FileProgramV1, Mode: 0o111, Access: jail.Exec}}
	for fd := 4; fd <= 7; fd++ {
		grants = append(grants, jail.FileRuleV1{FD: fd, Role: jail.FileDataV1, Mode: 0o444, Access: jail.Read})
	}
	binds := []jail.Bind{{File: directory, Path: vmDir, Write: true}, {Path: "/dev/urandom"}}
	if plan.Request.Driver == "nitro" {
		device, err := nitrodev.Open()
		if err != nil {
			return err
		}
		defer device.Close()
		binds = append(binds, jail.Bind{File: device, Path: nitrodev.Device, Write: true})
	} else {
		binds = append(binds, jail.Bind{Path: "/dev/kvm", Write: true})
	}
	command, err := jail.CommandSlotV1(context.Background(), jail.Spec{Argv: args, Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}, Files: append(runtimeFiles, disk), FileRulesV1: grants, Binds: binds, Rules: []jail.PathRule{{Path: vmDir, Access: jail.Connect}}, Net: jail.NetNone, Seccomp: jail.ProfileVMM}, lease)
	if err != nil {
		return err
	}
	// Bounded diagnostics live in the session's writable folder, without
	// any root write through a login-controlled pathname.
	log, err := os.OpenFile(vmDir+"/vmm.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND|unix.O_NOFOLLOW, 0o660)
	if err != nil {
		return err
	}
	defer log.Close()
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		if !command.PhysicalExitV1() {
			return retainKeeper(command, lock)
		}
		return err
	}
	init, err := command.RetainSlotInitV1(lease)
	if err != nil {
		_ = command.Signal(os.Kill)
		return retainKeeper(command, lock)
	}
	defer init.Close()
	ready, _ := json.Marshal(Response{OK: true, Slot: &plan.Slot, PID: command.Pid()})
	if err := unix.Sendmsg(int(reply.Fd()), append(ready, '\n'), unix.UnixRights(int(init.Fd()), int(executedVMM.Fd())), nil, unix.MSG_NOSIGNAL); err != nil {
		_ = command.Signal(os.Kill)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	reportExit := func() {
		waitErr := <-done
		code := command.ExitCode()
		response := Response{OK: true, ExitCode: &code}
		if waitErr != nil {
			response.Error = waitErr.Error()
		}
		var exit *jail.ExitError
		if errors.As(waitErr, &exit) && exit.Signal != 0 {
			response.ExitSignal = exit.Signal.String()
		}
		_ = json.NewEncoder(reply).Encode(response)
	}
	// The keeper stays alive after an uncertain wait and retains its lease.
	// A later stop retries this same owner; it never finds a saved PID.
	for {
		if command.PhysicalExitV1() {
			reportExit()
			return nil
		}
		if unixListener, ok := listener.(*net.UnixListener); ok {
			_ = unixListener.SetDeadline(time.Now().Add(time.Second))
		}
		connection, err := listener.Accept()
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			continue
		}
		if err != nil {
			return retainKeeper(command, lock)
		}
		if !rootPeer(connection) {
			connection.Close()
			continue
		}
		_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
		request, _, err := ReadRequest(connection)
		if err != nil || request.Op != "kill" || request.Session != plan.Request.Session {
			connection.Close()
			continue
		}
		_ = command.Signal(os.Kill)
		until := time.Now().Add(25 * time.Second)
		for !command.PhysicalExitV1() && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
		closed := command.PhysicalExitV1()
		_ = json.NewEncoder(connection).Encode(Response{OK: closed})
		connection.Close()
		if closed {
			reportExit()
			return nil
		}
	}
}

func retainKeeper(command *jail.Cmd, lock *os.File) error {
	for !command.PhysicalExitV1() {
		_ = command.Signal(os.Kill)
		time.Sleep(time.Second)
	}
	return refused("the keeper control socket ended before native cleanup")
}

func rootPeer(connection net.Conn) bool {
	unixConn, ok := connection.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return false
	}
	var credential *unix.Ucred
	if raw.Control(func(fd uintptr) { credential, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }) != nil {
		return false
	}
	return err == nil && credential != nil && credential.Uid == 0
}

func fixedCommand(r Request, dir string) ([]string, []byte, error) {
	if r.validate() != nil {
		return nil, nil, refused("the VMM template has invalid inputs")
	}
	cmdline := fmt.Sprintf("ro quiet root=/dev/dm-0 rootfstype=erofs init=/init dm-mod.waitfor=/dev/vda,/dev/vdb dm-mod.create=\"tools,,0,ro,0 %d verity 1 /dev/vda /dev/vda 4096 4096 %d %d sha256 %s %s 1 panic_on_corruption;image,,1,ro,0 %d verity 1 /dev/vdb /dev/vdb 4096 4096 %d %d sha256 %s %s 1 panic_on_corruption\" -- wall-init --microvm-v1", r.ToolsDataBlocks*8, r.ToolsDataBlocks, r.ToolsDataBlocks, r.ToolsRootHash, strings.Repeat("0", 64), r.ImageDataBlocks*8, r.ImageDataBlocks, r.ImageDataBlocks+1, r.ImageRootHash, strings.Repeat("0", 64))
	switch r.Driver {
	case "chv":
		args := []string{"/proc/self/fd/3", "--api-socket", "path=" + dir + "/api.sock", "--kernel", "/proc/self/fd/4", "--cmdline", cmdline, "--cpus", "boot=" + strconv.Itoa(r.CPUs), "--memory", fmt.Sprintf("size=%dM", r.MemoryMiB), "--balloon", "size=0,free_page_reporting=on", "--disk"}
		for i, serial := range []string{"tools", "image", "agents"} {
			args = append(args, fmt.Sprintf("path=/proc/self/fd/%d,readonly=on,direct=on,image_type=raw,backing_files=off,_disable_io_uring=on,_disable_aio=on,serial=%s", 5+i, serial))
		}
		args = append(args, "path=/proc/self/fd/8,readonly=off,direct=on,image_type=raw,backing_files=off,sparse=on,_disable_io_uring=on,_disable_aio=on,serial=session", "--vsock", "cid=3,socket="+dir+"/vsock.sock", "--serial", "off", "--console", "off", "--seccomp", "true", "--landlock")
		return args, nil, nil
	case "firecracker", "nitro":
		maximum := 32
		if r.Driver == "nitro" {
			maximum = 1024
		}
		if r.CPUs > maximum {
			return nil, nil, refused("the Firecracker request exceeds its CPU limit")
		}
		config := map[string]any{"boot-source": map[string]any{"kernel_image_path": "/proc/self/fd/4", "boot_args": cmdline}, "machine-config": map[string]any{"vcpu_count": r.CPUs, "mem_size_mib": r.MemoryMiB, "smt": false, "track_dirty_pages": false}, "network-interfaces": []any{}, "vsock": map[string]any{"guest_cid": 3, "uds_path": dir + "/vsock.sock"}}
		var drives []any
		for i, name := range []string{"tools", "image", "agents", "session"} {
			path := "/proc/self/fd/" + strconv.Itoa(5+i)
			drives = append(drives, map[string]any{"drive_id": name, "path_on_host": path, "is_root_device": false, "is_read_only": i != 3, "cache_type": "Writeback", "io_engine": "Sync"})
		}
		config["drives"] = drives
		if r.Driver == "nitro" {
			config["enclave"] = map[string]bool{"direct_mode": true, "cpu_overcommit": true}
		}
		data, err := json.Marshal(config)
		args := []string{"/proc/self/fd/3", "--config-file", dir + "/firecracker.json", "--api-sock", dir + "/api.sock", "--id", r.Session}
		if r.Driver == "nitro" {
			args = append(args, "--enable-pci")
		}
		return args, append(data, '\n'), err
	}
	return nil, nil, refused("the launcher has no fixed template for this driver")
}
