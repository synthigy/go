package main

// Regenerate the typed API in ./gen from the committed contract in ./synthigy.
// `go generate ./...` from this directory runs it offline (no server). To
// refresh the contract from a live server first, run: synthigy-gen -pull
//
//go:generate go run github.com/synthigy/go/cmd/synthigy-gen
