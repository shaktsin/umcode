package workbench

import (
	"encoding/json"
	"testing"
)

func TestImportEqualNumericValues(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{`{"eps":1e-05}`, `{"eps":0.00001}`, true},
		{`{"eps":1e-06}`, `{"eps":0.000001}`, true},
		{`[1.0,-0,100e-2]`, `[1,0,1]`, true},
		{`{"eps":1e-05}`, `{"eps":0.000001}`, false},
		{`9007199254740992`, `9007199254740993`, false},
		{`1e-10000`, `0`, false},
		{`"1"`, `1`, false},
	} {
		t.Run(tt.a+"/"+tt.b, func(t *testing.T) {
			if got := importEqual(json.RawMessage(tt.a), json.RawMessage(tt.b)); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
