package egress

import (
	"net/http"

	"github.com/iamlakshya/simstim/internal/brand"
)

func writeInspectedError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("X-Should-Retry", "false")
	w.Header().Set("Proxy-Status", brand.Name+"; details=\""+headerSanitiser.Replace(message)+"\"")
	http.Error(w, message, status)
}
