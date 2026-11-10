package workbench

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const hfDownloadLimit = 16 << 20

var hfRepoID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
var hfRevision = regexp.MustCompile(`^[a-f0-9]{40}$`)

// HuggingFace confines production requests to the Hub. BaseURL/HTTP permit a
// real local HTTP fixture in tests; neither is supplied through an RPC.
type HuggingFace struct {
	BaseURL string
	HTTP    *http.Client
}

type HFDataset struct {
	ID        string `json:"id"`
	Downloads int    `json:"downloads"`
	Likes     int    `json:"likes"`
	Private   bool   `json:"private"`
	Gated     bool   `json:"gated"`
}

type HFFile struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Supported bool   `json:"supported"`
}

type HFRepository struct {
	ID      string   `json:"id"`
	SHA     string   `json:"sha"`
	Private bool     `json:"private"`
	Gated   bool     `json:"gated"`
	Files   []HFFile `json:"files"`
	License string   `json:"license"`
}

func NewHuggingFace() *HuggingFace { return &HuggingFace{BaseURL: "https://huggingface.co"} }

func validHFFile(name string) bool {
	if name == "" || len(name) > 1024 || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

func supportedHFFile(name string) bool {
	if !validHFFile(name) {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".csv", ".tsv", ".jsonl", ".ndjson", ".txt", ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

func hfGated(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "false" && string(raw) != "null" && string(raw) != `""`
}

func (h *HuggingFace) read(ctx context.Context, endpoint, token string, limit int64) ([]byte, error) {
	base := strings.TrimRight(h.BaseURL, "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	origin, err := url.Parse(base)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("invalid Hugging Face service configuration")
	}
	if len(token) > 4096 || strings.ContainsFunc(token, unicode.IsSpace) {
		return nil, errors.New("invalid Hugging Face access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid Hugging Face request")
	}
	req.Header.Set("User-Agent", "UMCode-ML-Workbench/0.4")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 90 * time.Second}
	if h.HTTP != nil {
		client = *h.HTTP
		if client.Timeout == 0 {
			client.Timeout = 90 * time.Second
		}
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || next.URL.Scheme != "https" || next.URL.User != nil {
			return errors.New("unsupported Hugging Face redirect")
		}
		if next.URL.Host != via[0].URL.Host || via[len(via)-1].URL.Host != via[0].URL.Host {
			next.Header.Del("Authorization")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Hugging Face request failed; check your connection and try again")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case 401, 403:
			return nil, errors.New("Hugging Face access is required. Connect a read token and accept any dataset conditions on the Hub")
		case 404:
			return nil, errors.New("Hugging Face dataset or file was not found, or your account cannot access it")
		case 429:
			return nil, errors.New("Hugging Face rate limit reached; connect your account or try again later")
		default:
			return nil, fmt.Errorf("Hugging Face returned HTTP %d; try again later", resp.StatusCode)
		}
	}
	if resp.ContentLength > limit {
		return nil, errors.New("Hugging Face response exceeds this import's size limit")
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("Hugging Face download was interrupted; try again")
	}
	if int64(len(buf)) > limit {
		return nil, errors.New("Hugging Face response exceeds this import's size limit")
	}
	return buf, nil
}

func (h *HuggingFace) Search(ctx context.Context, query, token string) ([]HFDataset, error) {
	query = strings.TrimSpace(query)
	if len(query) > 256 || strings.ContainsFunc(query, unicode.IsControl) {
		return nil, errors.New("dataset search is too long or contains invalid characters")
	}
	q := url.Values{"search": {query}, "limit": {"12"}, "sort": {"downloads"}, "direction": {"-1"}}
	buf, err := h.read(ctx, "/api/datasets?"+q.Encode(), token, 1<<20)
	if err != nil {
		return nil, err
	}
	var records []struct {
		ID        string          `json:"id"`
		Downloads int             `json:"downloads"`
		Likes     int             `json:"likes"`
		Private   bool            `json:"private"`
		Gated     json.RawMessage `json:"gated"`
	}
	if json.Unmarshal(buf, &records) != nil {
		return nil, errors.New("Hugging Face returned an invalid dataset catalog")
	}
	items := make([]HFDataset, 0, len(records))
	for _, r := range records {
		if hfRepoID.MatchString(r.ID) {
			items = append(items, HFDataset{r.ID, r.Downloads, r.Likes, r.Private, hfGated(r.Gated)})
		}
		if len(items) >= 12 {
			break
		}
	}
	return items, nil
}

func (h *HuggingFace) Inspect(ctx context.Context, repo, revision, token string) (HFRepository, error) {
	if !hfRepoID.MatchString(repo) || revision != "" && revision != "main" && !hfRevision.MatchString(revision) {
		return HFRepository{}, errors.New("choose a Hugging Face dataset as owner/name and a valid commit revision")
	}
	endpoint := "/api/datasets/" + repo
	if revision != "" {
		endpoint += "/revision/" + revision
	}
	buf, err := h.read(ctx, endpoint+"?files_metadata=true", token, 4<<20)
	if err != nil {
		return HFRepository{}, err
	}
	var raw struct {
		ID       string          `json:"id"`
		SHA      string          `json:"sha"`
		Private  bool            `json:"private"`
		Gated    json.RawMessage `json:"gated"`
		CardData struct {
			License string `json:"license"`
		} `json:"cardData"`
		Siblings []struct {
			Name string `json:"rfilename"`
			Size int64  `json:"size"`
			LFS  *struct {
				Size int64 `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if json.Unmarshal(buf, &raw) != nil || !hfRevision.MatchString(raw.SHA) || raw.ID != repo {
		return HFRepository{}, errors.New("Hugging Face returned invalid repository metadata")
	}
	info := HFRepository{ID: raw.ID, SHA: raw.SHA, Private: raw.Private, Gated: hfGated(raw.Gated), License: raw.CardData.License, Files: []HFFile{}}
	for _, s := range raw.Siblings {
		if !validHFFile(s.Name) {
			continue
		}
		size := s.Size
		if s.LFS != nil {
			size = s.LFS.Size
		}
		if size < 0 {
			return HFRepository{}, errors.New("invalid Hugging Face file size")
		}
		info.Files = append(info.Files, HFFile{s.Name, size, supportedHFFile(s.Name)})
		if len(info.Files) > 10000 {
			return HFRepository{}, errors.New("this repository has too many files for this Data-tab release")
		}
	}
	sort.Slice(info.Files, func(i, j int) bool { return info.Files[i].Name < info.Files[j].Name })
	return info, nil
}

func (h *HuggingFace) Download(ctx context.Context, source DataSource, token string) (DataSource, []DataFile, error) {
	if (source.Kind != "huggingface" && source.Kind != "example") || !hfRepoID.MatchString(source.Repo) || len(source.Files) == 0 || len(source.Files) > 1000 {
		return source, nil, errors.New("choose a repository and at least one supported data file")
	}
	seen := map[string]bool{}
	for _, name := range source.Files {
		if !supportedHFFile(name) || seen[name] {
			return source, nil, errors.New("choose distinct CSV, TSV, JSONL, text, PNG, or JPEG files; dataset scripts and archives are not supported")
		}
		seen[name] = true
	}
	info, err := h.Inspect(ctx, source.Repo, source.Revision, token)
	if err != nil {
		return source, nil, err
	}
	listed := map[string]HFFile{}
	for _, file := range info.Files {
		listed[file.Name] = file
	}
	var total int64
	for _, name := range source.Files {
		file, ok := listed[name]
		if !ok || !file.Supported {
			return source, nil, errors.New("the selected file is not available in this dataset revision")
		}
		total += file.Size
		if total > hfDownloadLimit {
			return source, nil, errors.New("selected files exceed the 16 MiB import limit; choose a smaller dataset or subset")
		}
	}
	source.Revision = info.SHA
	source.Files = append([]string(nil), source.Files...)
	sort.Strings(source.Files)
	files := make([]DataFile, 0, len(source.Files))
	total = 0
	for _, name := range source.Files {
		parts := strings.Split(name, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		buf, err := h.read(ctx, "/datasets/"+source.Repo+"/resolve/"+info.SHA+"/"+strings.Join(parts, "/"), token, hfDownloadLimit-total)
		if err != nil {
			return source, nil, err
		}
		total += int64(len(buf))
		files = append(files, DataFile{Name: name, ContentBase64: base64.StdEncoding.EncodeToString(buf)})
	}
	return source, files, nil
}

func (h *HuggingFace) Whoami(ctx context.Context, token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errors.New("enter a Hugging Face read token")
	}
	buf, err := h.read(ctx, "/api/whoami-v2", token, 64<<10)
	if err != nil {
		return "", err
	}
	var identity struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(buf, &identity) != nil || identity.Type != "user" || identity.Name == "" || len(identity.Name) > 100 {
		return "", errors.New("Hugging Face did not return a valid user account")
	}
	return identity.Name, nil
}
