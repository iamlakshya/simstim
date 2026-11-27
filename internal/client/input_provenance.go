package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/files"
)

// These private facts come only from the local planner. A public row,
// destination name, origin claim or caller boolean cannot construct them.
type inputSourceKind uint8

const (
	inputSourceUnknown inputSourceKind = iota
	inputSourceNonRepo
	inputSourceRepository
)

type inputSourceFact struct {
	kind   inputSourceKind
	origin string
	// localRoot and identity are private provenance, never wire or logs.
	localRoot string
	identity  os.FileInfo
	include   bool // additionally requires the same reviewed exposure proof
}

// repositoryMarker does not run Git. A failed metadata lookup never becomes
// proof that a source is non-repository. Bare markers are treated as repository
// provenance too, so missing origins cannot bypass the shared-host Repo ACL.
func repositoryMarker(source string) (string, error) {
	for directory := source; ; directory = filepath.Dir(directory) {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return "", err
		}
		_, markerErr := root.Lstat(".git")
		if markerErr == nil {
			root.Close()
			return directory, nil
		}
		if !errors.Is(markerErr, os.ErrNotExist) {
			root.Close()
			return "", markerErr
		}
		_, headErr := root.Lstat("HEAD")
		objects, objectErr := root.Lstat("objects")
		root.Close()
		if headErr == nil && objectErr == nil && objects.IsDir() {
			return directory, nil
		}
		if headErr != nil && !errors.Is(headErr, os.ErrNotExist) || objectErr != nil && !errors.Is(objectErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect local repository metadata")
		}
		if directory == filepath.Dir(directory) {
			return "", nil
		}
	}
}

func copySourceProvenance(ctx context.Context, copy plannedCopy, repositories map[string]inputSourceFact, metadata ...LocalGitMetadataOpener) (inputSourceFact, error) {
	root, err := copy.root.open()
	if err != nil {
		return inputSourceFact{}, err
	}
	defer root.Close()
	var file *os.File
	if copy.file.Directory {
		file, err = files.OpenContainedDirectory(root, copy.name)
	} else {
		file, err = files.OpenContainedRegular(root, copy.name)
	}
	if err != nil {
		return inputSourceFact{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, copy.info) {
		return inputSourceFact{}, copyInputError("copy_changed", "the source changed before its attribution check")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root.Name(), filepath.FromSlash(copy.name)))
	if err != nil {
		return inputSourceFact{}, err
	}
	repository, err := repositoryMarker(filepath.Dir(resolved))
	if err != nil {
		return inputSourceFact{}, err
	}
	if repository == "" {
		return inputSourceFact{kind: inputSourceNonRepo, localRoot: root.Name(), identity: copy.root.info}, nil
	}
	if cached, exists := repositories[repository]; exists {
		current, err := os.Stat(repository)
		if err != nil || !os.SameFile(current, cached.identity) {
			return inputSourceFact{}, copyInputError("input_attribution", "the source repository changed during planning")
		}
		return cached, nil
	}
	fact, err := repositorySourceProvenance(ctx, repository, metadata...)
	if err == nil {
		repositories[repository] = fact
	}
	return fact, err
}

func repositorySourceProvenance(ctx context.Context, directory string, metadata ...LocalGitMetadataOpener) (_ inputSourceFact, err error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return inputSourceFact{}, err
	}
	defer root.Close()
	before, err := root.Stat(".")
	if err != nil {
		return inputSourceFact{}, err
	}
	git, err := openLocalGit(ctx, directory, metadata...)
	if err != nil {
		return inputSourceFact{}, err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	// A marker is only discovery. Native Git must confirm actual metadata.
	if _, err = git.run(ctx, nil, 4096, "rev-parse", "--absolute-git-dir"); err != nil {
		return inputSourceFact{}, errors.Join(copyInputError("input_attribution", "the repository source cannot be verified"), err)
	}
	origin, err := git.run(ctx, nil, 64<<10, "config", "--get", "remote.origin.url")
	if gitExit(err, 1) {
		origin, err = nil, nil
	}
	if err != nil {
		return inputSourceFact{}, err
	}
	after, err := os.Stat(directory)
	if err != nil || !os.SameFile(before, after) {
		return inputSourceFact{}, copyInputError("input_attribution", "the source repository changed during attribution")
	}
	return inputSourceFact{kind: inputSourceRepository, origin: gitLine(origin), localRoot: directory, identity: before}, nil
}
