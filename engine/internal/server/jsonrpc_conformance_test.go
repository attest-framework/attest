package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type jsonrpcFixture struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Lines           []string `json:"lines"`
	ExpectedReplies []struct {
		ID        json.RawMessage `json:"id"`
		ErrorCode *int            `json:"error_code"`
	} `json:"expected_replies"`
}

type jsonrpcReply struct {
	ID    json.RawMessage `json:"id"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}

// TestJSONRPCConformance replays protocol-tests/jsonrpc through a real server
// and requires exactly the expected replies, in order.
func TestJSONRPCConformance(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "protocol-tests", "jsonrpc", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no jsonrpc fixtures found: %v", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fx jsonrpcFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Run(fx.Name, func(t *testing.T) {
			var out bytes.Buffer
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			srv := New(strings.NewReader(strings.Join(fx.Lines, "\n")+"\n"), &out, logger)
			if err := RegisterBuiltinHandlers(srv); err != nil {
				t.Fatal(err)
			}
			if err := srv.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			var replies []jsonrpcReply
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				if line == "" {
					continue
				}
				var r jsonrpcReply
				if err := json.Unmarshal([]byte(line), &r); err != nil {
					t.Fatalf("reply %q: %v", line, err)
				}
				if r.ID == nil {
					t.Fatalf("reply %q has no id member", line)
				}
				replies = append(replies, r)
			}
			if len(replies) != len(fx.ExpectedReplies) {
				t.Fatalf("got %d replies, want %d:\n%s", len(replies), len(fx.ExpectedReplies), out.String())
			}
			for i, want := range fx.ExpectedReplies {
				got := replies[i]
				if string(got.ID) != string(want.ID) {
					t.Errorf("reply %d id = %s, want %s", i, got.ID, want.ID)
				}
				switch {
				case want.ErrorCode == nil && got.Error != nil:
					t.Errorf("reply %d: unexpected error %d", i, got.Error.Code)
				case want.ErrorCode != nil && got.Error == nil:
					t.Errorf("reply %d: want error %d, got success", i, *want.ErrorCode)
				case want.ErrorCode != nil && got.Error.Code != *want.ErrorCode:
					t.Errorf("reply %d: error %d, want %d", i, got.Error.Code, *want.ErrorCode)
				}
			}
		})
	}
}
