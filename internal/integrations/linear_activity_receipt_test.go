package integrations

import (
	"encoding/json"
	"testing"
)

func TestLinearActivityReceiptAcceptsProviderLinkFormatting(t *testing.T) {
	wanted := linearActivity{ID: "activity", Session: "session", Actor: "actor", Content: json.RawMessage(`{"type":"elicitation","body":"Review the request.\n\n[Open request](http://localhost:18080/requests/request)"}`)}
	for _, test := range []struct {
		name, content string
		want          bool
	}{
		{"provider link formatting", `{"type":"elicitation","body":"Review the request.\n\n[Open request](<http://localhost:18080/requests/request>)"}`, true},
		{"changed destination", `{"type":"elicitation","body":"Review the request.\n\n[Open request](<https://other.example/requests/request>)"}`, false},
		{"changed body", `{"type":"elicitation","body":"Different request.\n\n[Open request](<http://localhost:18080/requests/request>)"}`, false},
		{"changed type", `{"type":"response","body":"Review the request.\n\n[Open request](<http://localhost:18080/requests/request>)"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := linearActivityReceipt{ID: wanted.ID, Content: json.RawMessage(test.content)}
			receipt.Session.ID, receipt.User.ID = wanted.Session, wanted.Actor
			if got := activityReceiptMatches(receipt, wanted); got != test.want {
				t.Fatalf("receipt match = %v; want %v", got, test.want)
			}
		})
	}
}

func TestLinearActivityReceiptAcceptsProviderListMarkers(t *testing.T) {
	for _, test := range []struct {
		name, wanted, received string
		matches                bool
	}{
		{"top-level list", "Checks performed:\n\n- First check\n- Second check", "Checks performed:\n\n* First check\n* Second check", true},
		{"list and link", "- `go test ./...` passed\n\n[Run](http://localhost:18080/runs/run)", "* `go test ./...` passed\n\n[Run](<http://localhost:18080/runs/run>)", true},
		{"changed item", "- Check passed", "* Check failed", false},
		{"changed whitespace", "- Check passed", "*  Check passed", false},
		{"backtick code", "```text\n- literal\n```", "```text\n* literal\n```", false},
		{"tilde code", "~~~\n- literal\n~~~", "~~~\n* literal\n~~~", false},
		{"indented code", "    - literal", "    * literal", false},
		{"tabbed code", "\t- literal", "\t* literal", false},
		{"longer fence", "````\n```\n- literal\n````", "````\n```\n* literal\n````", false},
		{"list after fence", "```\n- literal\n```\n\n- Check", "```\n- literal\n```\n\n* Check", true},
		{"unfinished fence", "```\n- literal", "```\n* literal", false},
		{"thematic break", "- - -", "* - -", false},
		{"HTML code", "<pre>\n- literal\n</pre>", "<pre>\n* literal\n</pre>", false},
		{"converted thematic break", "- **", "* **", false},
		{"setext heading", "Heading\n- ", "Heading\n* ", false},
		{"non-ASCII fence suffix", "```\n```\u00a0\n- literal\n```", "```\n```\u00a0\n* literal\n```", false},
		{"CRLF thematic break", "Checks\r\n\r\n- - -\r\n", "Checks\r\n\r\n* - -\r\n", false},
		{"CRLF fence then list", "```\r\n- literal\r\n```\r\n\r\n- Check", "```\r\n- literal\r\n```\r\n\r\n* Check", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wantedBody, _ := json.Marshal(map[string]string{"type": "response", "body": test.wanted})
			receivedBody, _ := json.Marshal(map[string]string{"type": "response", "body": test.received})
			wanted := linearActivity{ID: "activity", Session: "session", Actor: "actor", Content: wantedBody}
			receipt := linearActivityReceipt{ID: wanted.ID, Content: receivedBody}
			receipt.Session.ID, receipt.User.ID = wanted.Session, wanted.Actor
			if got := activityReceiptMatches(receipt, wanted); got != test.matches {
				t.Fatalf("receipt match=%v, want %v", got, test.matches)
			}
		})
	}
}
