package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// proxyHost is one stored Proxy Host, in the data file's shape.
type proxyHost struct {
	ID            int       `json:"id"`
	Enabled       bool      `json:"enabled"`
	Domains       []string  `json:"domains"`
	Forward       forward   `json:"forward"`
	Exposure      string    `json:"exposure"`
	ForceSSL      bool      `json:"forceSsl"`
	CertResolver  string    `json:"certResolver"`
	Middlewares   []string  `json:"middlewares"`
	SkipCertCheck bool      `json:"skipCertCheck"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// forward is a Proxy Host's Forward Target.
type forward struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

// URL is where the Forward Target is reached, e.g. http://192.168.1.10:5000.
func (f forward) URL() string {
	return fmt.Sprintf("%s://%s:%d", f.Scheme, f.Host, f.Port)
}

// dataFile is the shape of proxy-hosts.json.
type dataFile struct {
	Version    int         `json:"version"`
	NextID     int         `json:"nextId"`
	ProxyHosts []proxyHost `json:"proxyHosts"`
}

// store holds the Proxy Hosts in one JSON file, the single source of truth.
// It is the file's only writer; every write replaces the file atomically.
// It stamps createdAt/updatedAt, and updatedAt is each host's version.
type store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	data dataFile
}

// openStore loads the data file in dir. A missing file means zero hosts.
func openStore(dir string, now func() time.Time) (*store, error) {
	s := &store{path: filepath.Join(dir, "proxy-hosts.json"), now: now, data: dataFile{Version: 1, NextID: 1, ProxyHosts: []proxyHost{}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("%s won't parse: %w", s.path, err)
	}
	// A restored file may carry a lower nextId; never hand out a used ID.
	for _, h := range s.data.ProxyHosts {
		s.data.NextID = max(s.data.NextID, h.ID+1)
	}
	return s, nil
}

var (
	errNotFound = errors.New("no such Proxy Host")
	errStale    = errors.New("Proxy Host changed since that version")
)

// add stores h as a new Proxy Host with the next ID.
func (s *store) add(h proxyHost) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	h.ID = next.NextID
	next.NextID++
	h.CreatedAt = s.now().UTC()
	h.UpdatedAt = h.CreatedAt
	next.ProxyHosts = append(slices.Clone(next.ProxyHosts), h)
	return s.save(next)
}

// update applies change to Proxy Host id and saves it. With a non-nil seen,
// the host must still be at that version, or update refuses with errStale.
func (s *store) update(id int, seen *time.Time, change func(*proxyHost)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return errNotFound
	}
	if seen != nil && !s.data.ProxyHosts[i].UpdatedAt.Equal(*seen) {
		return errStale
	}
	next := s.data
	next.ProxyHosts = slices.Clone(next.ProxyHosts)
	change(&next.ProxyHosts[i])
	next.ProxyHosts[i].UpdatedAt = s.now().UTC()
	return s.save(next)
}

// delete removes Proxy Host id. Its ID is never handed out again.
func (s *store) delete(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return errNotFound
	}
	next := s.data
	next.ProxyHosts = slices.Delete(slices.Clone(next.ProxyHosts), i, i+1)
	return s.save(next)
}

// get returns Proxy Host id.
func (s *store) get(id int) (proxyHost, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i >= 0 {
		return s.data.ProxyHosts[i], true
	}
	return proxyHost{}, false
}

// index is the position of Proxy Host id, or -1.
func (s *store) index(id int) int {
	return slices.IndexFunc(s.data.ProxyHosts, func(h proxyHost) bool { return h.ID == id })
}

// save writes next to disk and only then makes it current.
func (s *store) save(next dataFile) error {
	if err := s.write(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// write replaces the data file with d via a temp file and rename.
func (s *store) write(d dataFile) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".proxy-hosts-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// all returns a copy of every stored Proxy Host.
func (s *store) all() []proxyHost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.data.ProxyHosts)
}
