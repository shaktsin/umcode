package retrieval

import (
	"database/sql"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"reflect"
	"strings"
	"testing"
)

func TestBuildQueryBoundedAndSafe(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE VIRTUAL TABLE docs USING fts5(body); INSERT INTO docs(body) VALUES('compiler Packet src/main.go')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		in    string
		terms []string
	}{
		{"compiler Packet", []string{"compiler", "packet"}},
		{"compiler compiler", []string{"compiler"}},
		{"\" OR NEAR() * :", nil},
		{"", nil}, {"\xff", nil},
	} {
		t.Run(tc.in, func(t *testing.T) {
			q := BuildQuery(tc.in, nil)
			if !reflect.DeepEqual(q.Terms, tc.terms) {
				t.Fatalf("terms=%q want %q", q.Terms, tc.terms)
			}
			if q.FTS != "" {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM docs WHERE docs MATCH ?`, q.FTS).Scan(&n); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	q := BuildQuery("compiler missing", nil)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM docs WHERE docs MATCH ?`, q.FTS).Scan(&n); err != nil || n != 1 {
		t.Fatalf("OR query matched %d: %v", n, err)
	}
	q = BuildQuery(strings.Repeat("word ", 2000)+"outside", []string{"active Task"})
	if len(q.Terms) > 16 || strings.Contains(q.FTS, "outside") {
		t.Fatalf("unbounded: %+v", q)
	}
	q = BuildQuery(strings.Repeat("界", 100), nil)
	for _, term := range q.Terms {
		if len(term) > 64 {
			t.Fatal("oversized term")
		}
	}
	q = BuildQuery("src/main.go BuildPacket", nil)
	if !reflect.DeepEqual(q.Paths, []string{"src/main.go"}) || !reflect.DeepEqual(q.Symbols, []string{"BuildPacket"}) {
		t.Fatalf("hints=%+v", q)
	}
}
