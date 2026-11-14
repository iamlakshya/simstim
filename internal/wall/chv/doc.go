// Package chv implements the Cloud Hypervisor microvm wall. It starts only
// client-authorised runtime bytes within the canonical VMM jail and the
// already admitted session unit. Its host side dials the guest; it never
// creates a guest-to-host listener or accepts guest-selected runtime paths.
package chv
