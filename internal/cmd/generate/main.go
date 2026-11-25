// Command generate writes types_gen.go and resources_gen.go from contract/orbtr-v1.json; with -check it
// writes nothing and fails when a file differs. Run from the module root (go generate ./... does).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/ORBTR/sdk-go/internal/gen"
)

func main() {
	check := flag.Bool("check", false, "fail when a generated file is not what the contract generates")
	flag.Parse()
	data, err := os.ReadFile("contract/orbtr-v1.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	files, err := gen.Generate(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	stale := false
	for _, name := range gen.SortedNames(files) {
		current, _ := os.ReadFile(name)
		if bytes.Equal(current, files[name]) {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "%s is not what contract/orbtr-v1.json generates; run go generate ./...\n", name)
			stale = true
			continue
		}
		if err := os.WriteFile(name, files[name], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("wrote", name)
	}
	if stale {
		os.Exit(1)
	}
}
