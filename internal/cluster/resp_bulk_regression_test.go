package cluster

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadRESPBulkRejectsOversizedLength(t *testing.T) {
	for _, wire := range []string{
		"$9223372036854775807\r\n",
		"$1099511627776\r\nabc",
	} {
		payload, errRead := readRESPBulk(bufio.NewReader(strings.NewReader(wire)))
		if errRead == nil {
			t.Fatalf("readRESPBulk(%q) = %q, want error", wire, payload)
		}
	}
}

func TestReadRESPBulkRejectsOverlongLine(t *testing.T) {
	wire := "+" + strings.Repeat("a", 2*clusterRESPMaxLineBytes) + "\r\n"
	if _, errRead := readRESPBulk(bufio.NewReader(strings.NewReader(wire))); errRead == nil {
		t.Fatal("readRESPBulk() accepted an overlong line")
	}
}

func TestReadRESPBulkReadsPayload(t *testing.T) {
	payload, errRead := readRESPBulk(bufio.NewReader(strings.NewReader("$5\r\nhello\r\n")))
	if errRead != nil {
		t.Fatal(errRead)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q, want hello", payload)
	}
}
