package workbench

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const hfTestSHA = "1234567890abcdef1234567890abcdef12345678"

func TestHuggingFacePinnedFileImport(t *testing.T) {
	var paths []string
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer hf_test_read" {
			t.Error("missing expected read credential")
		}
		switch r.URL.Path {
		case "/api/datasets/nprak26/example/revision/" + hfTestSHA:
			fmt.Fprintf(w, `{"id":"nprak26/example","sha":%q,"private":false,"gated":false,"cardData":{"license":"cc-by-4.0"},"siblings":[{"rfilename":"data.csv","size":20},{"rfilename":"part.parquet","size":100}]}`, hfTestSHA)
		case "/datasets/nprak26/example/resolve/" + hfTestSHA + "/data.csv":
			fmt.Fprint(w, "age,label\n42,yes\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	h := &HuggingFace{BaseURL: s.URL, HTTP: s.Client()}
	info, err := h.Inspect(context.Background(), "nprak26/example", hfTestSHA, "hf_test_read")
	if err != nil || info.SHA != hfTestSHA || len(info.Files) != 2 || !info.Files[0].Supported || info.Files[1].Supported {
		t.Fatalf("inspect = %+v, %v", info, err)
	}
	source, files, err := h.Download(context.Background(), DataSource{Kind: "huggingface", Name: "Example", Repo: "nprak26/example", Revision: hfTestSHA, Files: []string{"data.csv"}}, "hf_test_read")
	if err != nil || source.Revision != hfTestSHA || len(files) != 1 {
		t.Fatalf("download = %+v %+v, %v", source, files, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(files[0].ContentBase64)
	if err != nil || string(decoded) != "age,label\n42,yes\n" {
		t.Fatalf("downloaded bytes = %q, %v", decoded, err)
	}
	if paths[len(paths)-1] != "/datasets/nprak26/example/resolve/"+hfTestSHA+"/data.csv" {
		t.Fatalf("download not pinned: %v", paths)
	}
}

func TestHuggingFaceRejectsUnsafeRepositoryAndFileBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{}`) }))
	defer s.Close()
	h := &HuggingFace{BaseURL: s.URL, HTTP: s.Client()}
	for _, repo := range []string{"../secret", "https://other.example/data", "owner/repo/extra", "owner/repo?token=x"} {
		if _, err := h.Inspect(context.Background(), repo, "", ""); err == nil {
			t.Errorf("accepted repository %q", repo)
		}
	}
	for _, name := range []string{"../data.csv", "/data.csv", `dir\data.csv`, "dataset.py"} {
		if _, _, err := h.Download(context.Background(), DataSource{Kind: "huggingface", Repo: "owner/repo", Revision: hfTestSHA, Files: []string{name}}, ""); err == nil {
			t.Errorf("accepted file %q", name)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("unsafe input made %d requests", calls.Load())
	}
}

func TestHuggingFaceSearchAndIdentityParseRealResponse(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/whoami-v2" {
			fmt.Fprint(w, `{"type":"user","name":"nprak26"}`)
			return
		}
		if r.URL.Query().Get("search") != "nprak26" || r.URL.Query().Get("limit") != "12" {
			t.Errorf("bad search query %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `[{"id":"nprak26/data","downloads":25,"likes":2,"private":false,"gated":"auto"}]`)
	}))
	defer s.Close()
	h := &HuggingFace{BaseURL: s.URL, HTTP: s.Client()}
	items, err := h.Search(context.Background(), "nprak26", "")
	if err != nil || len(items) != 1 || items[0].ID != "nprak26/data" || !items[0].Gated {
		t.Fatalf("search = %+v, %v", items, err)
	}
	name, err := h.Whoami(context.Background(), "hf_test_read")
	if err != nil || name != "nprak26" {
		t.Fatalf("identity = %q, %v", name, err)
	}
}

func TestHuggingFaceAccessErrorsNeverEchoCredentialsOrResponseBody(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "private response hf_secret_canary")
			}))
			defer s.Close()
			h := &HuggingFace{BaseURL: s.URL, HTTP: s.Client()}
			_, err := h.Search(context.Background(), "test", "hf_secret_canary")
			if err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "private response") {
				t.Fatalf("unsafe error = %v", err)
			}
			if (status == 401 || status == 403) && !strings.Contains(strings.ToLower(err.Error()), "access") {
				t.Fatalf("missing access guidance: %v", err)
			}
		})
	}
}

func TestHuggingFaceRedirectDropsAuthorizationForDifferentOrigin(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization") != "")
		fmt.Fprint(w, `[]`)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/data", http.StatusFound)
	}))
	defer origin.Close()
	h := &HuggingFace{BaseURL: origin.URL, HTTP: origin.Client()}
	if _, err := h.Search(context.Background(), "test", "hf_secret_canary"); err != nil {
		t.Fatal(err)
	}
	if leaked.Load() {
		t.Fatal("credential reached a different origin")
	}
}

func TestHuggingFaceDownloadChecksMembershipAndSize(t *testing.T) {
	var downloads atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fmt.Fprintf(w, `{"id":"owner/repo","sha":%q,"siblings":[{"rfilename":"large.csv","size":16777217}]}`, hfTestSHA)
			return
		}
		downloads.Add(1)
		fmt.Fprint(w, "unexpected")
	}))
	defer s.Close()
	h := &HuggingFace{BaseURL: s.URL, HTTP: s.Client()}
	for _, name := range []string{"missing.csv", "large.csv"} {
		if _, _, err := h.Download(context.Background(), DataSource{Kind: "huggingface", Repo: "owner/repo", Files: []string{name}}, ""); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if downloads.Load() != 0 {
		t.Fatal("download started despite invalid selection/size")
	}
}
