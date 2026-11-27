// Command userns-policy writes the namespace launcher's compile-time policy.
package main

import (
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Error: select amd64 or arm64.\nTry: use cmd/userns/build.sh.")
		os.Exit(1)
	}
	p, err := process.SeccompProgram(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("/* Generated from internal/wall/process. Do not edit. */\n#define HELPER_PATH %q\n#define GUEST_RUN %q\n", "/opt/"+brand.Name+"/bin/"+brand.HostBinary, "/run/"+brand.Name)
	fmt.Printf("#define HELPER_DIR %q\n", "/opt/"+brand.Name+"/bin")
	fmt.Printf("#define DNS_MARKER %q\n", brand.Env("DNS_FDS"))
	fmt.Printf("#define JAIL_ARGV0 %q\n#define NATIVE_TREQUEST %d\n#define NATIVE_TRESPONSE %d\n", brand.Name+"-jail", proto.TRequest, proto.TResponse)
	fmt.Println("#ifdef PROCESS_FILTER")
	fmt.Println("static const struct sock_filter process_filter[] = {")
	for _, s := range p {
		fmt.Printf("{%d,%d,%d,%d},\n", s.Code, s.JT, s.JF, s.K)
	}
	fmt.Println("};")
	fmt.Println("#endif")
}
