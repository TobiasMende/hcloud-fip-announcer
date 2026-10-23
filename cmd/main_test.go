package main

import (
	"strings"
	"testing"
)

func TestValidClusterID(t *testing.T) {
	cases := map[string]bool{
		"prod":                  true,
		"a":                     true,
		"Prod-1_eu.central":     true,
		strings.Repeat("a", 63): true,
		"":                      false,
		strings.Repeat("a", 64): false,
		"-prod":                 false,
		"prod.":                 false,
		"prod cluster":          false,
		"prod/1":                false,
		"präd":                  false,
	}
	for id, want := range cases {
		if got := validClusterID(id); got != want {
			t.Errorf("validClusterID(%q) = %v, want %v", id, got, want)
		}
	}
}
