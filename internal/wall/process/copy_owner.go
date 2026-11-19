package process

// CopyOwnerV1 is issued only by this package's actual started process factory.
// Its private native resources are never reconstructed from a manifest, guest
// descriptor, caller path, device equality or callback.
type CopyOwnerV1 struct{ native *copyOwnerNativeV1 }
