package cache

import (
	"reflect"
	"testing"
)

func TestSplitCacheProg(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantName string
		wantArgs []string
		wantErr  bool
	}{
		{"bare binary", "helper", "helper", nil, false},
		// Regression: a helper with flags must split into program + args,
		// not be exec'd as one path.
		{"binary with args", "helper --backend s3 --dir /tmp/c", "helper", []string{"--backend", "s3", "--dir", "/tmp/c"}, false},
		{"absolute path with args", "/usr/local/bin/helper -stats=true", "/usr/local/bin/helper", []string{"-stats=true"}, false},
		{"surrounding whitespace", "  helper  -v  ", "helper", []string{"-v"}, false},
		{"tab separator", "helper\t-v", "helper", []string{"-v"}, false},
		{"double-quoted path with space", `"/opt/my helper/bin" -v`, "/opt/my helper/bin", []string{"-v"}, false},
		{"single-quoted arg with space", `helper --msg 'hello world'`, "helper", []string{"--msg", "hello world"}, false},
		{"empty", "", "", nil, true},
		{"whitespace only", "   ", "", nil, true},
		{"unterminated quote", `helper "abc`, "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, err := splitCacheProg(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("splitCacheProg(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			// Treat nil and empty as equal so a bare binary (no args) passes.
			if len(args) != 0 || len(tt.wantArgs) != 0 {
				if !reflect.DeepEqual(args, tt.wantArgs) {
					t.Errorf("args = %#v, want %#v", args, tt.wantArgs)
				}
			}
		})
	}
}
