package main

import "os"

// osGetenv exists so oauth.go can be unit-tested via a swappable
// getenv hook without monkey-patching os.Getenv.
func osGetenv(key string) string { return os.Getenv(key) }
