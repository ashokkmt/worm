package ingest

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func TestReadBoundedFrameLimitsTerminatedAndUnterminatedData(t *testing.T) {
	for _, input := range []string{"12345678\n", "123456789012345"} {
		if _, err := readBoundedFrame(bufio.NewReader(strings.NewReader(input)), '\n', 8); !errors.Is(err, errFrameTooLarge) {
			t.Errorf("input length %d error = %v, want frame limit", len(input), err)
		}
	}
	got, err := readBoundedFrame(bufio.NewReader(strings.NewReader("1234\n")), '\n', 5)
	if err != nil || string(got) != "1234\n" {
		t.Fatalf("bounded frame = %q, err=%v", got, err)
	}
}
