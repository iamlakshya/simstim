// launchmeta writes or verifies a release envelope from the actual launcher
// executable. Signing stays with the existing protected release workflow.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	binary := flag.String("binary", "", "actual static launcher executable")
	arch := flag.String("arch", "", "release architecture: amd64 or arm64")
	version := flag.String("version", "", "release semantic version")
	output := flag.String("output", "", "envelope filename")
	signature := flag.String("verify-signature", "", "verify the existing envelope's SSHSIG signature")
	flag.Parse()
	if flag.NArg() != 0 || *output == "" {
		return fmt.Errorf("the launcher envelope needs an output filename\nTry: pass -binary, -arch, -version and -output.")
	}
	if *signature != "" {
		meta, err := os.ReadFile(*output)
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(*signature)
		if err != nil {
			return err
		}
		_, err = slotlaunch.VerifyEnvelope(meta, sig)
		return err
	}
	file, err := os.Open(*binary)
	if err != nil {
		return err
	}
	defer file.Close()
	meta, err := slotlaunch.MakeEnvelope(file, *arch, *version)
	if err != nil {
		return err
	}
	return os.WriteFile(*output, meta, 0o644)
}
