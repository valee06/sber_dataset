package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DB — простое файловое хранилище (JSON) для операций и журнала загрузок.
// Состояние БД меняется только при подтверждении импорта или правке операции.
type DB struct {
	mu      sync.RWMutex
	path    string
	Ops     []Operation    `json:"ops"`
	Imports []ImportRecord `json:"imports"`
}

func OpenDB(path string) (*DB, error) {
	d := &DB{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DB) saveLocked() error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}

// HasID сообщает, есть ли уже операция с таким идентификатором.
func (d *DB) HasIDs() map[string]bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	m := make(map[string]bool, len(d.Ops))
	for _, o := range d.Ops {
		m[o.ID] = true
	}
	return m
}

// AddImport добавляет операции и запись в журнал; дубликаты пропускаются.
func (d *DB) AddImport(rec ImportRecord, ops []Operation) (ImportRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	exist := make(map[string]bool, len(d.Ops))
	for _, o := range d.Ops {
		exist[o.ID] = true
	}
	rec.ID = newID()
	for _, o := range ops {
		if exist[o.ID] {
			rec.Skipped++
			continue
		}
		o.ImportID = rec.ID
		d.Ops = append(d.Ops, o)
		rec.Total++
		if o.Direction == DirIn {
			rec.InCount++
			rec.InSum += o.AmountKop
		} else {
			rec.OutCount++
			rec.OutSum += o.AmountKop
		}
	}
	d.Imports = append(d.Imports, rec)
	return rec, d.saveLocked()
}

// Get возвращает операцию по идентификатору.
func (d *DB) Get(id string) (Operation, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, o := range d.Ops {
		if o.ID == id {
			return o, true
		}
	}
	return Operation{}, false
}

// Update правит категорию, лицевой счёт и период операции вручную.
func (d *DB) Update(id, category, ls, period string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.Ops {
		if d.Ops[i].ID == id {
			d.Ops[i].Category = category
			d.Ops[i].LS = ls
			d.Ops[i].Period = period
			d.Ops[i].Manual = true
			return d.saveLocked()
		}
	}
	return errors.New("операция не найдена")
}

// Reset очищает базу (используется флагом -reset).
func (d *DB) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Ops, d.Imports = nil, nil
	return d.saveLocked()
}

// LastImport возвращает последнюю загрузку.
func (d *DB) LastImport() *ImportRecord {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.Imports) == 0 {
		return nil
	}
	r := d.Imports[len(d.Imports)-1]
	return &r
}

// ImportsDesc — журнал загрузок, новые сверху.
func (d *DB) ImportsDesc() []ImportRecord {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]ImportRecord, len(d.Imports))
	for i, r := range d.Imports {
		out[len(out)-1-i] = r
	}
	return out
}

// Filter — условия отбора операций.
type Filter struct {
	Year, Month, Dir, LS, Cat, Q string
	InOnly                       bool // только поступления («Платежи»)
}

// Result — отобранные операции и итоги по ним.
type Result struct {
	Items            []Operation // новые сверху
	InCount, OutCnt  int
	InSum, OutSum    int64
	Linked, LinkedMn int // поступления с лицевым счётом / из них поправлены вручную
}

func (f Filter) match(o *Operation, q string) bool {
	if f.InOnly && o.Direction != DirIn {
		return false
	}
	if f.Year != "" && !strings.HasPrefix(o.Date, f.Year) {
		return false
	}
	if f.Month != "" && (len(o.Date) < 7 || o.Date[5:7] != f.Month) {
		return false
	}
	if f.Dir != "" && o.Direction != f.Dir {
		return false
	}
	switch f.LS {
	case "":
	case "-":
		if o.LS != "" || o.Direction != DirIn {
			return false
		}
	default:
		if o.LS != f.LS {
			return false
		}
	}
	if f.Cat != "" && o.Category != f.Cat {
		return false
	}
	if q != "" {
		hay := strings.ToLower(o.Counterparty + " " + o.Purpose + " " + o.DocNo + " " + o.CounterINN + " " + o.Via)
		if o.LS != "" {
			hay += " лс " + o.LS
		}
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// Query отбирает операции по фильтру.
func (d *DB) Query(f Filter) Result {
	d.mu.RLock()
	defer d.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(f.Q))
	var r Result
	for i := range d.Ops {
		o := &d.Ops[i]
		if !f.match(o, q) {
			continue
		}
		r.Items = append(r.Items, *o)
		if o.Direction == DirIn {
			r.InCount++
			r.InSum += o.AmountKop
			if o.LS != "" {
				r.Linked++
				if o.Manual {
					r.LinkedMn++
				}
			}
		} else {
			r.OutCnt++
			r.OutSum += o.AmountKop
		}
	}
	sort.SliceStable(r.Items, func(i, j int) bool {
		a, b := r.Items[i], r.Items[j]
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return a.Time > b.Time
	})
	return r
}

// Facets — значения для выпадающих списков фильтров.
type Facets struct {
	Years []string
	LS    []string
	Cats  []string
	From  string
	To    string
	Total int
	LSNum int
}

func (d *DB) Facets() Facets {
	d.mu.RLock()
	defer d.mu.RUnlock()
	years := map[string]bool{}
	ls := map[string]bool{}
	cats := map[string]bool{}
	var f Facets
	for _, o := range d.Ops {
		if len(o.Date) >= 4 {
			years[o.Date[:4]] = true
		}
		if o.LS != "" {
			ls[o.LS] = true
		}
		cats[o.Category] = true
		if f.From == "" || o.Date < f.From {
			f.From = o.Date
		}
		if o.Date > f.To {
			f.To = o.Date
		}
	}
	for y := range years {
		f.Years = append(f.Years, y)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(f.Years)))
	f.LS = sortedLS(ls)
	f.LSNum = len(ls)
	for _, c := range AllCategories() {
		if cats[c] {
			f.Cats = append(f.Cats, c)
			delete(cats, c)
		}
	}
	var rest []string
	for c := range cats {
		rest = append(rest, c)
	}
	sort.Strings(rest)
	f.Cats = append(f.Cats, rest...)
	f.Total = len(d.Ops)
	return f
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- сессии загрузки (черновик импорта) ----

// StagedOp — операция в черновике импорта.
type StagedOp struct {
	Operation
	Duplicate bool `json:"duplicate"`
}

// Staging — разобранная, но ещё не сохранённая в БД выписка конкретной сессии.
// Хранится в каталоге данных, поэтому переживает закрытие страницы.
type Staging struct {
	SID      string     `json:"sid"`
	File     string     `json:"file"`
	At       time.Time  `json:"at"`
	Account  string     `json:"account"`
	Org      string     `json:"org"`
	From     string     `json:"from"`
	To       string     `json:"to"`
	Ops      []StagedOp `json:"ops"`
	Skipped  int        `json:"skipped"`
	Warnings []string   `json:"warnings,omitempty"`
}

// Stagings — хранилище черновиков по идентификатору сессии.
type Stagings struct {
	mu  sync.Mutex
	dir string
}

func NewStagings(dir string) *Stagings { return &Stagings{dir: dir} }

func (s *Stagings) file(sid string) string {
	// идентификатор сессии — только hex, защита от выхода за каталог
	for _, r := range sid {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	if sid == "" || len(sid) > 64 {
		return ""
	}
	return filepath.Join(s.dir, sid+".json")
}

func (s *Stagings) Get(sid string) *Staging {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.file(sid)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var st Staging
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return &st
}

func (s *Stagings) Put(st *Staging) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.file(st.SID)
	if p == "" {
		return errors.New("некорректная сессия")
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func (s *Stagings) Delete(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.file(sid); p != "" {
		_ = os.Remove(p)
	}
}

// Cleanup удаляет черновики старше maxAge.
func (s *Stagings) Cleanup(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, _ := os.ReadDir(s.dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

func newSID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
