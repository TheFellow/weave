package adapter

import (
	"bytes"
	"testing"
)

func FuzzParseFrames(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("{not-json}\n"))
	f.Add([]byte(`{"protocol":"weave.adapter/v0","request_id":"request","kind":"unknown","payload":{}}` + "\n"))

	capabilities := Capabilities{Provider: Provider{Name: "fuzz", Version: "1"}, FactEncoding: FactEncoding}
	limits := Limits{MaxFrameBytes: 4 << 10, MaxTotalBytes: 8 << 10, MaxFrames: 100, MaxFacts: 100, MaxDiagnostics: 20}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = parseFrames(bytes.NewReader(input), "request", capabilities, limits.withDefaults())
	})
}
