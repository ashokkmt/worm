package main

import (
	"fmt"
	"io"
	"os"
	"testing"
)

func TestVersionFlags(t *testing.T) {
	for _, arg := range []string{"-v", "--version"} {
		t.Run(arg, func(t *testing.T) {
			oldArgs, oldStdout := os.Args, os.Stdout
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Args = []string{"worm", arg}
			os.Stdout = write
			main()
			_ = write.Close()
			os.Args, os.Stdout = oldArgs, oldStdout
			defer read.Close()

			got, err := io.ReadAll(read)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("WORM CLI Version: %s\n", Version)
			if string(got) != want {
				t.Fatalf("version output = %q, want %q", got, want)
			}
		})
	}
}
