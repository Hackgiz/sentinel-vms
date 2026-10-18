package main

// sentinel -verify-export <zip>: recompute an evidence package's hashes
// offline and print a verdict (WAVE8-DETECT-EVIDENCE.md, Stream C).

import (
	"flag"
	"fmt"
	"io"
	"os"

	"sentinel-linux/internal/evidencepkg"
)

// verifyExportPath registers the flag at package level so main.go needs
// only one line (exitIfVerifyExport) after flag.Parse.
var verifyExportPath = flag.String("verify-export", "", "verify an evidence package zip (media SHA-256 and audit chain against its manifest) and exit 0 if intact, 1 if not")

func exitIfVerifyExport() {
	if *verifyExportPath != "" {
		os.Exit(runVerifyExport(*verifyExportPath, os.Stdout, os.Stderr))
	}
}

// runVerifyExport prints the verdict for the zip at path: 0 verified, 1
// tampered or unreadable.
func runVerifyExport(path string, out, errOut io.Writer) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(errOut, "verify-export:", err)
		return 1
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fmt.Fprintln(errOut, "verify-export:", err)
		return 1
	}
	v, err := evidencepkg.Verify(f, st.Size())
	if err != nil {
		fmt.Fprintln(errOut, "UNVERIFIED  "+err.Error())
		return 1
	}
	fmt.Fprintln(out, v.Summary())
	if !v.OK {
		return 1
	}
	return 0
}
