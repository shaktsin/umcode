// Command umcode-compute is a private executable shipped inside the desktop
// app. It intentionally has no general-purpose CLI: the engine sends one
// versioned request over stdin and this process launches one disposable VM.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/shaktsin/umcode/internal/compute"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "run" || os.Args[2] != "--request-stdin" {
		fmt.Fprintln(os.Stderr, "umcode-compute is an internal app component")
		os.Exit(2)
	}
	dec := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
	dec.DisallowUnknownFields()
	var req compute.GuestRequest
	if err := dec.Decode(&req); err != nil {
		fail(err)
	}
	if err := req.Validate(); err != nil {
		fail(err)
	}
	if err := compute.RunGuest(req); err != nil {
		fail(err)
	}
}

func fail(err error) {
	if err == nil {
		err = errors.New("unknown compute error")
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(125)
}
