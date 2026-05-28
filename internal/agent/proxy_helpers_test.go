package agent

import (
	"io"
	"strings"
	"testing"
)

func TestRenameJSONFieldAnyRenamesFirstMatchingField(t *testing.T) {
	transform := RenameJSONFieldAny([]string{"decision", "reply"}, "behavior")
	body := transform(io.NopCloser(strings.NewReader(`{"requestID":"req-1","reply":"reject"}`)))
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"requestID":"req-1","behavior":"reject"}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
