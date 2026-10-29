package infra

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store keeps plans, the inventory and the ledger in a folder:
//
//	plans/<id>.json   each plan, with its steps as they ran
//	resources.json    what NullBot made, and its state
//	ledger.jsonl      every proposal, decision and command, appended
type Store struct {
	dir string
	mu  sync.Mutex
}

// Open returns the store in dir, created on first write.
func Open(dir string) *Store { return &Store{dir: dir} }

// Dir is the store's folder.
func (s *Store) Dir() string { return s.dir }

// LedgerEntry is one line of the ledger.
type LedgerEntry struct {
	Time     time.Time `json:"time"`
	Plan     string    `json:"plan"`
	Event    string    `json:"event"` // proposed, approved, rejected, step, failed, applied
	Step     string    `json:"step,omitempty"`
	Summary  string    `json:"summary,omitempty"`
	Argv     []string  `json:"argv,omitempty"`
	Exit     int       `json:"exit,omitempty"`
	Output   string    `json:"output,omitempty"`
	Error    string    `json:"error,omitempty"`
	Resource string    `json:"resource,omitempty"`
	By       string    `json:"by,omitempty"`
}

// lock serializes writers across processes (the desktop app and a
// headless NullBot) with a lock file; a lock older than two minutes is
// left over from a crash and is taken over.
func (s *Store) lock() (func(), error) {
	s.mu.Lock()
	if err := os.MkdirAll(filepath.Join(s.dir, "plans"), 0o700); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	path := filepath.Join(s.dir, "lock")
	deadline := time.Now().Add(15 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path); s.mu.Unlock() }, nil
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Minute {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			s.mu.Unlock()
			return nil, errors.New("another NullBot is changing the infrastructure records; try again")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) planPath(id string) (string, error) {
	if !strings.HasPrefix(id, "plan-") || strings.ContainsAny(id, `/\.`) || len(id) > 40 {
		return "", fmt.Errorf("%q is not a plan id", id)
	}
	return filepath.Join(s.dir, "plans", id+".json"), nil
}

// SavePlan writes a plan.
func (s *Store) SavePlan(p Plan) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.savePlanLocked(p)
}

func (s *Store) savePlanLocked(p Plan) error {
	path, err := s.planPath(p.ID)
	if err != nil {
		return err
	}
	p.Hash = ""
	return writeJSON(path, p)
}

// Plan reads one plan.
func (s *Store) Plan(id string) (Plan, error) {
	path, err := s.planPath(id)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, fmt.Errorf("there is no plan %s", id)
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	p.Hash = p.ContentHash()
	return p, nil
}

// Plans lists plans, newest first.
func (s *Store) Plans(limit int) ([]Plan, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "plans"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var plans []Plan
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p, err := s.Plan(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			plans = append(plans, p)
		}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Created.After(plans[j].Created) })
	if limit > 0 && len(plans) > limit {
		plans = plans[:limit]
	}
	return plans, nil
}

// Resources lists the inventory.
func (s *Store) Resources() ([]Resource, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "resources.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Resource
	return out, json.Unmarshal(data, &out)
}

// UpdateResources changes the inventory under the lock.
func (s *Store) UpdateResources(fn func([]Resource) []Resource) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.updateResourcesLocked(fn)
}

func (s *Store) updateResourcesLocked(fn func([]Resource) []Resource) error {
	current, err := s.Resources()
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.dir, "resources.json"), fn(current))
}

// Log appends to the ledger.
func (s *Store) Log(entry LedgerEntry) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.logLocked(entry)
}

func (s *Store) logLocked(entry LedgerEntry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "ledger.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// Ledger returns the last limit entries, oldest first, optionally for one
// plan.
func (s *Store) Ledger(plan string, limit int) ([]LedgerEntry, error) {
	f, err := os.Open(filepath.Join(s.dir, "ledger.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LedgerEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var e LedgerEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil || (plan != "" && e.Plan != plan) {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) > 2*limit {
			out = out[len(out)-limit:]
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, scanner.Err()
}

// Propose saves a new plan and records it.
func (s *Store) Propose(p Plan) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.savePlanLocked(p); err != nil {
		return err
	}
	return s.logLocked(LedgerEntry{Plan: p.ID, Event: "proposed", Summary: p.Title, By: p.Requester})
}

// Decide approves or rejects a proposed plan. Only a person decides: the
// caller is a user interface or the command line, never an agent's tool.
// seen is the Hash of the plan the person looked at; approving fails if
// the plan changed since, and the approval covers exactly that content,
// sealed with key (see Plan.Seal).
func (s *Store) Decide(id string, approve bool, by, seen string, key []byte) (Plan, error) {
	if approve && len(key) == 0 {
		return Plan{}, errors.New("approving needs NullBot's approval key from the vault, which is not available")
	}
	unlock, err := s.lock()
	if err != nil {
		return Plan{}, err
	}
	defer unlock()
	p, err := s.Plan(id)
	if err != nil {
		return p, err
	}
	if p.State != PlanProposed {
		return p, fmt.Errorf("plan %s is %s, not waiting for a decision", id, p.State)
	}
	if approve && seen != "" && seen != p.ContentHash() {
		return p, fmt.Errorf("plan %s changed since you looked at it; look at it again before approving", id)
	}
	p.State, p.Decided, p.DecidedBy = PlanRejected, time.Now().UTC(), by
	event := "rejected"
	if approve {
		p.State, event = PlanApproved, "approved"
		p.Approval = p.Seal(key, by)
	}
	if err := s.savePlanLocked(p); err != nil {
		return p, err
	}
	return p, s.logLocked(LedgerEntry{Plan: id, Event: event, Summary: p.Title, By: by})
}
