package guest

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// WriteBrowserURLs is the trusted guest browser helper's entire action.
// URLs are printed with terminal controls removed. It opens no browser,
// listener, callback forwarding rule or network connection.
func WriteBrowserURLs(out io.Writer, args []string) error {
	for _, value := range args {
		if _, err := fmt.Fprintln(out, api.CleanText(value, len(value))); err != nil {
			return fmt.Errorf("print guest browser URL: %w", err)
		}
	}
	return nil
}
