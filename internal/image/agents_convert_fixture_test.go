//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"archive/tar"
	"io"
	"time"
)

func writeBadAgentsTar(output io.Writer, kind string) error {
	writer := tar.NewWriter(output)
	for _, name := range []string{"bin", "root"} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755, Uid: 1000, Gid: 1000, ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
	}
	header := &tar.Header{Name: "root/entry", Typeflag: tar.TypeReg, Mode: 0o755, Uid: 1000, Gid: 1000, ModTime: time.Unix(0, 0)}
	switch kind {
	case "wrong-owner":
		header.Uid = 0
	case "absolute":
		header.Name = "/root/entry"
	case "traversal":
		header.Name = "root/../outside"
	case "device":
		header.Typeflag = tar.TypeChar
	case "duplicate":
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	return writer.Close()
}
