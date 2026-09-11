// Command supervisedharness is the "SDK's own process" the supervised-stdio
// contract test (../../auth_supervised_test.go) spawns under a stub parent.
// It talks to the synthigy package through its PUBLIC surface only (New,
// Client.Token, Client.Search, Audience, Selection, Error) — exactly what an
// external consumer would use — and reports its result as one line of JSON
// on stderr, keeping stdout exclusively the auth.token protocol channel
// under test. Lives under testdata/ so `go build ./...`/`go vet ./...` on
// the SDK module skip it; the test builds it explicitly by path.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	synthigy "github.com/synthigy/go"
)

type result struct {
	OK     bool     `json:"ok"`
	Token  string   `json:"token,omitempty"`
	Tokens []string `json:"tokens,omitempty"`
	Error  string   `json:"error,omitempty"`
	Code   string   `json:"code,omitempty"`
}

func report(r result) {
	b, _ := json.Marshal(r)
	fmt.Fprintln(os.Stderr, string(b))
}

func errResult(err error) result {
	r := result{OK: false, Error: err.Error()}
	var se *synthigy.Error
	if errors.As(err, &se) {
		r.Code = se.Code
	}
	return r
}

func main() {
	if len(os.Args) < 2 {
		report(result{OK: false, Error: "missing mode arg"})
		return
	}
	ctx := context.Background()

	switch os.Args[1] {
	case "ask":
		c, err := synthigy.New(synthigy.Config{Endpoint: "http://unused.invalid"})
		if err != nil {
			report(errResult(err))
			return
		}
		tok, err := c.Token(ctx)
		if err != nil {
			report(errResult(err))
			return
		}
		report(result{OK: true, Token: tok})

	case "ask-twice":
		c, err := synthigy.New(synthigy.Config{Endpoint: "http://unused.invalid"})
		if err != nil {
			report(errResult(err))
			return
		}
		t1, err := c.Token(ctx, synthigy.Audience("aud-a"))
		if err != nil {
			report(errResult(err))
			return
		}
		t2, err := c.Token(ctx, synthigy.Audience("aud-b"))
		if err != nil {
			report(errResult(err))
			return
		}
		report(result{OK: true, Tokens: []string{t1, t2}})

	case "search":
		if len(os.Args) < 3 {
			report(result{OK: false, Error: "search mode requires an endpoint arg"})
			return
		}
		c, err := synthigy.New(synthigy.Config{Endpoint: os.Args[2]})
		if err != nil {
			report(errResult(err))
			return
		}
		if _, err := c.Search(ctx, "user", nil, synthigy.Selection{"x": nil}); err != nil {
			report(errResult(err))
			return
		}
		report(result{OK: true})

	default:
		report(result{OK: false, Error: "unknown mode " + os.Args[1]})
	}
}
