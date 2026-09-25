package decode

import (
	"fmt"
	"testing"
)

func FuzzAllFormatDecoders(f *testing.F) {
	seeds := [][]byte{
		[]byte(`<14>1 2026-09-24T12:00:00Z host app - - msg`),
		[]byte(`{"event":"login","nested":{"ok":true}}`),
		[]byte("a,b\\n1,2\\n"),
		[]byte(`CEF:0|Acme|IDS|1|7|Alert|5|src=10.0.0.1`),
		[]byte(`<event><source>sensor</source></event>`),
		[]byte("LEEF:2.0|Acme|IAM|1|login|x09|usrName=alice\\tsrc=10.0.0.1"),
		[]byte(`10.0.0.1 - - [20/Sep/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 1`),
		[]byte(`<14>Sep 24 12:00:00 host CEF:0|Acme|IDS|1|7|Alert|5|src=10.0.0.1`),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	decoders := []Decoder{NewSyslogDecoder(), NewJSONDecoder(), NewCSVDecoder(), NewCEFDecoder(), NewXMLDecoder(), NewLEEFDecoder(), NewTextDecoder()}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxPayloadBytes {
			t.Skip()
		}
		for _, d := range decoders {
			if d.Detect(data) > 0 {
				_, _ = d.Decode(data)
			}
		}
	})
}

func TestJSONDepthAndCEFFieldBounds(t *testing.T) {
	deep := []byte(`{"x":`)
	for i := 0; i < MaxDepth+1; i++ {
		deep = append(deep, '[')
	}
	deep = append(deep, '0')
	for i := 0; i < MaxDepth+1; i++ {
		deep = append(deep, ']')
	}
	deep = append(deep, '}')
	if _, err := NewJSONDecoder().Decode(deep); err == nil {
		t.Fatal("expected JSON depth rejection")
	}
	cef := `CEF:0|v|p|1|1|n|5|`
	for i := 0; i < MaxFields; i++ {
		cef += fmt.Sprintf(" k%d=v", i)
	}
	if _, err := NewCEFDecoder().Decode([]byte(cef)); err == nil {
		t.Fatal("expected CEF field bound rejection")
	}
}
