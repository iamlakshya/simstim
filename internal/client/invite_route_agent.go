package client

import (
	"errors"
	"os"
	"path/filepath"
)

// Agent custody is a restriction on an explicit user-owned bastion source.
// It never supplies an invite endpoint key or enables agent forwarding.
func openRouteSocketV1(path, expected string) (*explicitRouteCredentialV1, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	parent, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	file, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	err = errors.Join(routeDirectoryNativeSafeV1(file, parent), file.Close())
	if err != nil {
		root.Close()
		return nil, err
	}
	info, err := root.Lstat(filepath.Base(path))
	if err != nil {
		root.Close()
		return nil, err
	}
	native, err := routeSocketNativeIDV1(info)
	if err != nil || expected != "" && native != expected {
		root.Close()
		return nil, routeSourceErrorV1("the original SSH agent socket changed")
	}
	credential := &explicitRouteCredentialV1{path: path, info: info, socket: true, parent: root, parentInfo: parent}
	if err := credential.checkSocket(); err != nil {
		root.Close()
		return nil, err
	}
	return credential, nil
}
func (c *explicitRouteCredentialV1) checkSocket() error {
	if c == nil || !c.socket || c.parent == nil {
		return routeSourceErrorV1("the original SSH agent custody is missing")
	}
	parent, err := os.Stat(filepath.Dir(c.path))
	if err != nil || !os.SameFile(parent, c.parentInfo) {
		return routeSourceErrorV1("the original SSH agent parent changed")
	}
	current, err := os.Lstat(c.path)
	if err != nil {
		return err
	}
	retained, err := c.parent.Lstat(filepath.Base(c.path))
	if err != nil {
		return err
	}
	if !os.SameFile(current, c.info) || !os.SameFile(retained, c.info) {
		return routeSourceErrorV1("the original SSH agent socket was replaced")
	}
	_, err = routeSocketNativeIDV1(current)
	return err
}
