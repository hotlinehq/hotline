// Command compare prints the results of load runs side by side: see
// load/README.md. Usage: compare load/results
package main

import (
	"fmt"
	"os"

	"hotline-load/compare"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: compare <results folder>")
		os.Exit(2)
	}
	out, err := compare.Table(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare failed:", err)
		os.Exit(1)
	}
	fmt.Print(out)
}
