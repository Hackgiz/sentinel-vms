// Command linkprobe pokes a management server's control link the way a
// recording server would, for the e2e checks (checks.py, phase "revoke"):
//
//	linkprobe enroll -management https://127.0.0.1:28091 -token sen1.… -data DIR
//	    enroll into DIR/link; exit 0 on success, 1 with the refusal
//	linkprobe renew -data DIR
//	    renew the certificate of the identity in DIR/link; exit 0 on
//	    success, 1 with the refusal
//
// Output is one line, "ok …" or "refused <status>: <message>" (or
// "error: …" for anything that isn't an HTTP answer), so checks can match
// on it. Test tooling only; it never runs against a real site.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"sentinel-linux/internal/link/remote"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: linkprobe enroll|renew [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	mgmt := fs.String("management", "", "control link URL")
	token := fs.String("token", "", "enrollment token")
	data := fs.String("data", "", "data directory (identity in <data>/link)")
	_ = fs.Parse(os.Args[2:])
	if *data == "" {
		fmt.Fprintln(os.Stderr, "-data is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	switch os.Args[1] {
	case "enroll":
		var id *remote.Identity
		id, err = remote.Enroll(ctx, *data, remote.EnrollOptions{Management: *mgmt, Token: *token, Name: "linkprobe", Version: "e2e"})
		if err == nil {
			fmt.Println("ok enrolled", id.ServerID)
			return
		}
	case "renew":
		var id *remote.Identity
		id, err = remote.LoadIdentity(*data)
		if err == nil {
			err = id.Renew(ctx)
		}
		if err == nil {
			fmt.Println("ok renewed until", id.Cert().NotAfter.Format(time.DateOnly))
			return
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: linkprobe enroll|renew [flags]")
		os.Exit(2)
	}
	var he *remote.HTTPError
	if errors.As(err, &he) {
		fmt.Printf("refused %d: %s\n", he.Status, he.Message)
	} else {
		fmt.Println("error:", err)
	}
	os.Exit(1)
}
