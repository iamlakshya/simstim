package host

import (
	"errors"
	"net"
	"os"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/supply"
)

// nativeDescriptorRetirementsV1 owns first-call resource facts only. It
// contains no Purge, session, source, activation or commit authority.
type nativeDescriptorRetirementsV1 struct {
	mu          sync.Mutex
	files       map[*os.File]supply.FileCloseFactsV1
	roots       map[*os.Root]*nativeRootRetirementV1
	connections map[*net.UnixConn]supply.FileCloseFactsV1
	listeners   map[*net.UnixListener]supply.FileCloseFactsV1
}

type nativeRootRetirementV1 struct {
	root       *os.Root
	probe      *os.File
	probeFacts supply.FileCloseFactsV1
	facts      supply.FileCloseFactsV1
}

func nativeUnattemptedRetirementV1() supply.FileCloseFactsV1 {
	return supply.FileCloseFactsV1{Unknown: placementError("the original native close is in progress or has no first-call retirement proof")}
}

func nativeCheckRetirementFactsV1(facts supply.FileCloseFactsV1) error {
	if !facts.Retired || facts.Unknown != nil {
		return errors.Join(placementError("the original native descriptor retirement remains unknown"), facts.Unknown)
	}
	return nil
}

func (owner *nativeDescriptorRetirementsV1) closeFilesV4(files []*os.File) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.files == nil {
		owner.files = make(map[*os.File]supply.FileCloseFactsV1)
	}
	var failure error
	for _, file := range files {
		if file == nil {
			continue
		}
		facts, attempted := owner.files[file]
		if !attempted {
			// Publish custody before the one actual Close can fail or panic.
			owner.files[file] = nativeUnattemptedRetirementV1()
			facts = supply.CloseFileOnceV1(file)
			owner.files[file] = facts
		}
		failure = errors.Join(failure, facts.Diagnostic, nativeCheckRetirementFactsV1(facts))
	}
	return failure
}

func (owner *nativeDescriptorRetirementsV1) checkFilesV4(files []*os.File) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for _, file := range files {
		if file == nil {
			continue
		}
		facts, attempted := owner.files[file]
		if !attempted {
			return placementError("the original file has no retained first native close fact")
		}
		if err := nativeCheckRetirementFactsV1(facts); err != nil {
			return err
		}
	}
	return nil
}

// Real source owners must join every Root operation before this method.
// Go's Root closes an internal directory fd and suppresses its errno. A live
// original Root plus an unfiltered locked thread and those prior joins are
// therefore mandatory; an already-closed Root supplies no first-close fact.
func (owner *nativeDescriptorRetirementsV1) closeRootsV4(roots []*os.Root) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.roots == nil {
		owner.roots = make(map[*os.Root]*nativeRootRetirementV1)
	}
	var failure error
	for _, root := range roots {
		if root == nil {
			continue
		}
		record := owner.roots[root]
		if record == nil {
			record = &nativeRootRetirementV1{root: root, facts: nativeUnattemptedRetirementV1()}
			owner.roots[root] = record
			record.facts = closeOriginalNativeRootOnceV1(record)
		}
		failure = errors.Join(failure, record.facts.Diagnostic, nativeCheckRetirementFactsV1(record.facts))
	}
	return failure
}

func closeOriginalNativeRootOnceV1(record *nativeRootRetirementV1) supply.FileCloseFactsV1 {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := supply.CheckFileCloseThreadV1(); err != nil {
		return supply.FileCloseFactsV1{Unknown: err}
	}
	probe, err := record.root.Open(".")
	if probe != nil {
		record.probe = probe
	}
	if err != nil {
		return supply.FileCloseFactsV1{Unknown: errors.Join(placementError("the original Root was not live before its first native close"), err)}
	}
	probeFacts := supply.CloseFileOnceV1(probe)
	record.probeFacts = probeFacts
	if err := nativeCheckRetirementFactsV1(probeFacts); err != nil {
		return supply.FileCloseFactsV1{Diagnostic: probeFacts.Diagnostic, Unknown: err}
	}
	err = record.root.Close()
	if err != nil {
		return supply.FileCloseFactsV1{Diagnostic: errors.Join(probeFacts.Diagnostic, err), Unknown: err}
	}
	return supply.FileCloseFactsV1{Retired: true, Diagnostic: probeFacts.Diagnostic}
}

func (owner *nativeDescriptorRetirementsV1) checkRootsV4(roots []*os.Root) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for _, root := range roots {
		if root == nil {
			continue
		}
		record := owner.roots[root]
		if record == nil || record.root != root {
			return placementError("the original Root has no retained first native close fact")
		}
		if err := nativeCheckRetirementFactsV1(record.facts); err != nil {
			return err
		}
	}
	return nil
}

func (owner *nativeDescriptorRetirementsV1) diagnosticsV4() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	var diagnostics error
	for _, facts := range owner.files {
		diagnostics = errors.Join(diagnostics, facts.Diagnostic)
	}
	for _, record := range owner.roots {
		diagnostics = errors.Join(diagnostics, record.facts.Diagnostic)
	}
	for _, facts := range owner.connections {
		diagnostics = errors.Join(diagnostics, facts.Diagnostic)
	}
	for _, facts := range owner.listeners {
		diagnostics = errors.Join(diagnostics, facts.Diagnostic)
	}
	return diagnostics
}

func (owner *nativeDescriptorRetirementsV1) checkAllRetiredV4() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for _, facts := range owner.files {
		if err := nativeCheckRetirementFactsV1(facts); err != nil {
			return err
		}
	}
	for _, record := range owner.roots {
		if record == nil {
			return placementError("an original Root retirement record is absent")
		}
		if err := nativeCheckRetirementFactsV1(record.facts); err != nil {
			return err
		}
	}
	for _, facts := range owner.connections {
		if err := nativeCheckRetirementFactsV1(facts); err != nil {
			return err
		}
	}
	for _, facts := range owner.listeners {
		if err := nativeCheckRetirementFactsV1(facts); err != nil {
			return err
		}
	}
	return nil
}
