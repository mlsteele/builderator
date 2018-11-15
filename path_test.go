package main

import "testing"

// TestWhich doesn't assert, it's just for debugging.
func TestWhich(t *testing.T) {
	names := []string{
		"x",
		"less",
		"/usr/bin/less",
		"./less",
		"bin/less",
		"justasec",
		"logdump_viewer",
	}
	for _, name := range names {
		path, err := which(name)
		if err != nil {
			t.Fatal(err)
		}
		if path == nil {
			t.Logf("%v => %v", name, path)
		} else {
			t.Logf("%v => %v", name, *path)
		}
	}
}
