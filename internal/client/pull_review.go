package client

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client/review"
)

type changedObject struct{ path, old, new, oldMode, mode string }

func (g *pullGit) changedObjects(ctx context.Context, base, tip string) ([]changedObject, error) {
	data, err := g.run(ctx, nil, pullMetadataMax, "diff-tree", "-r", "--no-renames", "--no-commit-id", "--no-abbrev", "--raw", "-z", base, tip)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(data, []byte{0})
	var changes []changedObject
	for at := 0; at < len(fields); {
		if len(fields[at]) == 0 {
			at++
			continue
		}
		metadata := strings.Fields(string(fields[at]))
		at++
		if len(metadata) != 5 || !strings.HasPrefix(metadata[0], ":") || at >= len(fields) {
			return nil, pullProtocol("Git returned malformed changed-object metadata")
		}
		old, new := metadata[2], metadata[3]
		if !api.ValidGitOID(old) || !api.ValidGitOID(new) {
			return nil, pullProtocol("Git returned an invalid changed object ID")
		}
		changes = append(changes, changedObject{path: string(fields[at]), old: old, new: new, oldMode: strings.TrimPrefix(metadata[0], ":"), mode: metadata[1]})
		at++
	}
	return changes, nil
}

func zeroOID(oid string) bool { return strings.Trim(oid, "0") == "" }

func (g *pullGit) objectSize(ctx context.Context, oid string) (int64, error) {
	if zeroOID(oid) {
		return 0, nil
	}
	data, err := g.run(ctx, nil, 4096, "cat-file", "-s", oid)
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseInt(gitLine(data), 10, 64)
	if err != nil || size < 0 {
		return 0, pullProtocol("Git returned an invalid object size")
	}
	return size, nil
}

func (g *pullGit) blob(ctx context.Context, oid, mode string) ([]byte, error) {
	if zeroOID(oid) || mode == "000000" || mode == "160000" {
		return nil, nil
	}
	data, err := g.run(ctx, nil, int(review.FileLimit)+1, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > review.FileLimit {
		return nil, pullProtocol("a changed blob grew beyond its reviewed size")
	}
	return data, nil
}

func (g *pullGit) moduleValues(ctx context.Context, oid string) ([][]byte, error) {
	if zeroOID(oid) {
		return nil, nil
	}
	data, err := g.run(ctx, nil, pullMetadataMax, "config", "--no-includes", "--null", "--blob="+oid, "--list")
	if err != nil {
		return nil, &UserError{Msg: "the changed .gitmodules cannot be parsed safely", Hint: "Fix .gitmodules in the session and pull again.", Code: "pull_modules", Kind: KindRefused}
	}
	return gitConfigValues(data), nil
}

func gitConfigValues(data []byte) [][]byte {
	var values [][]byte
	for _, entry := range bytes.Split(data, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		_, value, found := bytes.Cut(entry, []byte{'\n'})
		if !found {
			value = nil
		}
		values = append(values, bytes.Clone(value))
	}
	return values
}

func addedPatchLines(data []byte) []byte {
	var added bytes.Buffer
	inHunk := false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("@@ ")) {
			inHunk = true
			continue
		}
		if !inHunk || len(line) == 0 {
			continue
		}
		if line[0] == '+' {
			added.Write(line[1:])
			added.WriteByte('\n')
		}
	}
	return added.Bytes()
}

func (g *pullGit) reviewChange(ctx context.Context, base, tip string, object changedObject) (review.Change, error) {
	change := review.Change{Path: object.path, Mode: object.mode, BeforeMode: object.oldMode, Deleted: zeroOID(object.new)}
	if !review.ValidMode(object.oldMode) || !review.ValidMode(object.mode) || strings.EqualFold(object.path, ".gitmodules") && (object.mode == "120000" || object.mode == "160000") {
		return change, nil
	}
	beforeSize, err := g.changedSize(ctx, object.old, object.oldMode)
	if err != nil {
		return change, err
	}
	afterSize, err := g.changedSize(ctx, object.new, object.mode)
	if err != nil {
		return change, err
	}
	change.Size = max(beforeSize, afterSize)
	// Size gates every content operation, including diff/textconv and module
	// parsing. The static scanner still flags the name and exact size.
	if change.Size > review.FileLimit {
		return change, nil
	}
	if change.Before, err = g.blob(ctx, object.old, object.oldMode); err != nil {
		return change, err
	}
	if change.After, err = g.blob(ctx, object.new, object.mode); err != nil {
		return change, err
	}
	if strings.EqualFold(object.path, ".gitmodules") {
		change.ModuleValues, err = g.moduleValues(ctx, object.new)
		if err != nil {
			return change, err
		}
		change.ModulesParsed = true
	}
	if change.Deleted {
		return change, nil
	}
	patch, err := g.run(ctx, nil, pullMetadataMax, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", "--no-color", base, tip, "--", object.path)
	if err != nil {
		return change, err
	}
	change.Added = addedPatchLines(patch)
	change.Binary = bytes.ContainsRune(change.After, 0) || !utf8.Valid(change.After)
	if len(change.Added) == 0 && !bytes.Equal(change.Before, change.After) {
		change.Added = bytes.Clone(change.After)
	}
	return change, nil
}

func (g *pullGit) review(ctx context.Context, base, tip string, scanner *review.Scanner) ([]review.Finding, int, error) {
	objects, err := g.changedObjects(ctx, base, tip)
	if err != nil {
		return nil, 0, err
	}
	var findings []review.Finding
	for _, object := range objects {
		change, err := g.reviewChange(ctx, base, tip, object)
		if err != nil {
			return nil, 0, err
		}
		flagged, err := scanner.Scan(change)
		if err != nil {
			return nil, 0, fmt.Errorf("review changed object: %w", err)
		}
		findings = append(findings, flagged...)
	}
	return findings, len(objects), nil
}

func (g *pullGit) changedSize(ctx context.Context, oid, mode string) (int64, error) {
	if mode == "160000" {
		return 0, nil
	}
	return g.objectSize(ctx, oid)
}
