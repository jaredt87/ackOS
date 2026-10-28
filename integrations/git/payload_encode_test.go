package git

import (
	"bytes"
	"testing"
)

func TestEncodeFilePayloadRoundTripsExactBytes(t *testing.T) {
	want := []byte{0x00, 0x01, 0xff, '\n'}
	payload, err := EncodeFilePayload("100755", want)
	if err != nil {
		t.Fatal(err)
	}
	mode, got, err := decodeFilePayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "100755" {
		t.Fatalf("mode = %q, want 100755", mode)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content = %v, want %v", got, want)
	}
}

func TestEncodeFilePayloadRejectsUnsupportedMode(t *testing.T) {
	if _, err := EncodeFilePayload("120000", []byte("target")); err == nil {
		t.Fatal("EncodeFilePayload accepted an unsupported mode")
	}
}
