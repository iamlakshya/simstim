package supply

import (
	"fmt"
	"math"
	"os"
)

// ContentRef names immutable host-created bytes, such as an agents disk.
// It creates no release kind or runtime delivery role.
type ContentRef struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Validate checks a bounded content reference without assigning a runtime role.
func (r ContentRef) Validate() error {
	if r.Name == "" || !IsHex64(r.SHA256) || r.Size <= 0 || r.Size == math.MaxInt64 {
		return fmt.Errorf("immutable content needs a name, canonical SHA-256 and positive bounded size")
	}
	return nil
}

// CheckContent applies the canonical size/SHA-256 checker to an already open
// regular single-link file. The cache owner supplies its path/mode/owner check.
func CheckContent(ref ContentRef, file *os.File) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if file == nil {
		return fmt.Errorf("immutable content descriptor is missing")
	}
	st, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat immutable content: %w", err)
	}
	if !st.Mode().IsRegular() || !singleLink(file, st) {
		return fmt.Errorf("immutable content is not a regular single-link file")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek immutable content: %w", err)
	}
	if err := checkBytes(ref.Name, ref.Size, ref.SHA256, st.Size(), file); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind checked immutable content: %w", err)
	}
	return nil
}
