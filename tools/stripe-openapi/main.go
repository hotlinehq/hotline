// Command stripe-openapi writes the OpenAPI 3.2 spec of Stripe's api from
// Stripe's own OpenAPI 3.0 spec, for the specs folder:
//
//	go run . -safe webhook_endpoint -secret key -secret token -secret confirmation_token source/spec3-2026-09-30.endive.yaml > ../../specs/stripe/api/2026-09-30.endive.openapi.yaml
//
// Each -safe NAME marks the path value NAME of every route safe
// (x-hotline-redact: false), so records show it. Records hide every other
// path value. Each -secret NAME marks it x-hotline-redact: true, so the
// file under content: full hides it too.
package main

import (
	"flag"
	"fmt"
	"os"

	"hotline-tools/stripe-openapi/stripe"
)

func main() {
	var options stripe.Options
	flag.Func("safe", "NAME: show the path value NAME in records", func(name string) error {
		options.SafePathValues = append(options.SafePathValues, name)
		return nil
	})
	flag.Func("secret", "NAME: hide the path value NAME under content: full too", func(name string) error {
		options.SecretPathValues = append(options.SecretPathValues, name)
		return nil
	})
	flag.Parse()
	if flag.NArg() != 1 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: stripe-openapi [-safe NAME]... [-secret NAME]... SPEC3.yaml > SPEC.openapi.yaml")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0)) //nolint:gosec // reads the spec the user names
	if err == nil {
		data, err = stripe.Convert(data, options)
	}
	if err == nil {
		_, err = os.Stdout.Write(data)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
