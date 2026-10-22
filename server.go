package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	pageSize      = 100
	maxUploadMB   = 64
	sessionCookie = "sid"
)

// App — зависимости веб-приложения.
type App struct {
	db         *DB
	st         *Stagings
	samplePath string
	tpl        map[string]*template.Template
}

var pageNames = []string{"home", "payments", "operations", "import", "edit"}

var monthNames = []string{"", "Январь", "Февраль", "Март", "Апрель", "Май", "Июнь", "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}

func (a *App) loadTemplates() error {
	funcs := template.FuncMap{
		"money":  money,
		"date":   fmtDate,
		"period": fmtPeriod,
		"dt":     fmtDateTime,
		"add":    func(a, b int) int { return a + b },
		"add64":  func(a, b int64) int64 { return a + b },
		"months": func() []string { return monthNames },
		"mnum":   func(i int) string { return fmt.Sprintf("%02d", i) },
		"dirLabel": func(d string) string {
			if d == DirIn {
				return "приход"
			}
			return "расход"
		},
		"cats": func(dir string) []string {
			if dir == DirIn {
				return InCategories
			}
			return OutCategories
		},
		"urlq": url.QueryEscape,
	}
	a.tpl = map[string]*template.Template{}
	for _, n := range pageNames {
		t, err := template.New(n).Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+n+".html")
		if err != nil {
			return err
		}
		a.tpl[n] = t
	}
	return nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(assets))
	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /payments", a.payments)
	mux.HandleFunc("GET /operations", a.operations)
	mux.HandleFunc("GET /import", a.importPage)
	mux.HandleFunc("POST /import/upload", a.importUpload)
	mux.HandleFunc("POST /import/sample", a.importSample)
	mux.HandleFunc("POST /import/commit", a.importCommit)
	mux.HandleFunc("POST /import/cancel", a.importCancel)
	mux.HandleFunc("GET /edit", a.editPage)
	mux.HandleFunc("POST /edit", a.editSave)
	return logRequests(mux)
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}

// ---- общие данные страниц ----

type NavItem struct{ Key, Href, Title, Sub string }

type Base struct {
	Active string
	Title  string
	Flash  string
	Error  string
	Nav    []NavItem
}

func newBase(active, title string, r *http.Request) Base {
	return Base{
		Active: active, Title: title,
		Flash: r.URL.Query().Get("msg"), Error: r.URL.Query().Get("err"),
		Nav: []NavItem{
			{"home", "/", "Главная", "Сводка по выписке"},
			{"payments", "/payments", "Платежи", "Поступления и квитанции"},
			{"operations", "/operations", "Операции по счёту", "Вся выписка: приход и расход"},
			{"import", "/import", "Импорт выписки", "Загрузка файла СберБизнес"},
		},
	}
}

func (a *App) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tpl[name].ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("шаблон %s: %v", name, err)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, path string, kv ...string) {
	u := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		u.Set(kv[i], kv[i+1])
	}
	if len(u) > 0 {
		path += "?" + u.Encode()
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// ---- форматирование ----

func money(k int64) string {
	neg := k < 0
	if neg {
		k = -k
	}
	rub := strconv.FormatInt(k/100, 10)
	var b strings.Builder
	for i, r := range rub {
		if i > 0 && (len(rub)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(r)
	}
	s := fmt.Sprintf("%s,%02d", b.String(), k%100)
	if neg {
		s = "−" + s
	}
	return s
}

func fmtDate(s string) string {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("02.01.2006")
	}
	return s
}

func fmtPeriod(s string) string {
	if len(s) == 7 {
		return "01." + s[5:7] + "." + s[:4]
	}
	return "—"
}

func fmtDateTime(t time.Time) string { return t.Local().Format("02.01.2006 15:04") }

// ---- фильтры и страницы списков ----

func filterFromQuery(r *http.Request, inOnly bool) Filter {
	q := r.URL.Query()
	return Filter{Year: q.Get("year"), Month: q.Get("month"), Dir: q.Get("dir"), LS: q.Get("ls"),
		Cat: q.Get("cat"), Q: q.Get("q"), InOnly: inOnly}
}

type ListPage struct {
	Base
	Path      string // /payments или /operations
	F         Filter
	Fac       Facets
	Res       Result
	Items     []Operation
	From, To  int // показаны записи From..To
	Total     int
	PrevURL   string
	NextURL   string
	Self      string // текущий адрес для возврата после правки
	NoData    bool
	LinkedPct int
}

func (a *App) list(w http.ResponseWriter, r *http.Request, name, path, title string, inOnly bool) {
	f := filterFromQuery(r, inOnly)
	res := a.db.Query(f)
	fac := a.db.Facets()
	page := atoiSafe(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	total := len(res.Items)
	start := (page - 1) * pageSize
	if start >= total {
		start, page = 0, 1
	}
	end := minInt(start+pageSize, total)
	p := ListPage{Base: newBase(name, title, r), Path: path, F: f, Fac: fac, Res: res,
		Items: res.Items[start:end], Total: total, NoData: fac.Total == 0, Self: r.URL.RequestURI()}
	if total > 0 {
		p.From, p.To = start+1, end
	}
	if res.InCount > 0 {
		p.LinkedPct = res.Linked * 100 / res.InCount
	}
	link := func(pg int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(pg))
		return path + "?" + q.Encode()
	}
	if page > 1 {
		p.PrevURL = link(page - 1)
	}
	if end < total {
		p.NextURL = link(page + 1)
	}
	a.render(w, name, p)
}

func (a *App) payments(w http.ResponseWriter, r *http.Request) {
	a.list(w, r, "payments", "/payments", "Платежи", true)
}

func (a *App) operations(w http.ResponseWriter, r *http.Request) {
	a.list(w, r, "operations", "/operations", "Операции по счёту", false)
}

// ---- главная ----

type HomePage struct {
	Base
	Empty     bool
	Fac       Facets
	Res       Result
	Last      *ImportRecord
	Recent    []Operation
	LinkedPct int
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	fac := a.db.Facets()
	res := a.db.Query(Filter{})
	p := HomePage{Base: newBase("home", "Главная", r), Empty: fac.Total == 0, Fac: fac, Res: res, Last: a.db.LastImport()}
	p.Recent = res.Items[:minInt(10, len(res.Items))]
	if in := a.db.Query(Filter{InOnly: true}); in.InCount > 0 {
		p.LinkedPct = in.Linked * 100 / in.InCount
		p.Res.Linked = in.Linked
	}
	a.render(w, "home", p)
}

// ---- импорт ----

type PreviewStats struct {
	InCount, OutCount, Dups, Selected int
	InSum, OutSum                     int64
}

type ImportPage struct {
	Base
	Stg       *Staging
	Stats     PreviewStats
	Journal   []ImportRecord
	InCats    []string
	OutCats   []string
	SampleOK  bool
	SampleNme string
}

func (a *App) sid(w http.ResponseWriter, r *http.Request, create bool) string {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if !create {
		return ""
	}
	sid := newSID()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
	return sid
}

func (a *App) importPage(w http.ResponseWriter, r *http.Request) {
	p := ImportPage{Base: newBase("import", "Импорт выписки", r), Journal: a.db.ImportsDesc(),
		InCats: InCategories, OutCats: OutCategories}
	if _, err := os.Stat(a.samplePath); err == nil {
		p.SampleOK, p.SampleNme = true, filepath.Base(a.samplePath)
	}
	if sid := a.sid(w, r, false); sid != "" {
		if st := a.st.Get(sid); st != nil {
			p.Stg = st
			for _, o := range st.Ops {
				if o.Direction == DirIn {
					p.Stats.InCount++
					p.Stats.InSum += o.AmountKop
				} else {
					p.Stats.OutCount++
					p.Stats.OutSum += o.AmountKop
				}
				if o.Duplicate {
					p.Stats.Dups++
				} else {
					p.Stats.Selected++
				}
			}
		}
	}
	a.render(w, "import", p)
}

// stage разбирает файл и кладёт результат в черновик текущей сессии (БД не меняется).
func (a *App) stage(w http.ResponseWriter, r *http.Request, path, displayName string) error {
	ps, err := ParseStatement(path, displayName)
	if err != nil {
		return err
	}
	have := a.db.HasIDs()
	st := &Staging{SID: a.sid(w, r, true), File: ps.FileName, At: time.Now(), Account: ps.Account, Org: ps.Org,
		From: ps.From, To: ps.To, Skipped: ps.Skipped, Warnings: ps.Warnings}
	for _, o := range ps.Ops {
		st.Ops = append(st.Ops, StagedOp{Operation: o, Duplicate: have[o.ID]})
	}
	return a.st.Put(st)
}

func (a *App) importUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadMB<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		redirect(w, r, "/import", "err", "Не удалось получить файл: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		redirect(w, r, "/import", "err", "Выберите файл выписки (.xlsx)")
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(hdr.Filename), ".xlsx") {
		redirect(w, r, "/import", "err", "Поддерживаются только файлы .xlsx")
		return
	}
	tmp, err := os.CreateTemp("", "statement-*.xlsx")
	if err != nil {
		redirect(w, r, "/import", "err", err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		redirect(w, r, "/import", "err", err.Error())
		return
	}
	tmp.Close()
	if err := a.stage(w, r, tmp.Name(), filepath.Base(hdr.Filename)); err != nil {
		redirect(w, r, "/import", "err", err.Error())
		return
	}
	redirect(w, r, "/import")
}

func (a *App) importSample(w http.ResponseWriter, r *http.Request) {
	if err := a.stage(w, r, a.samplePath, filepath.Base(a.samplePath)); err != nil {
		redirect(w, r, "/import", "err", err.Error())
		return
	}
	redirect(w, r, "/import")
}

func (a *App) importCancel(w http.ResponseWriter, r *http.Request) {
	if sid := a.sid(w, r, false); sid != "" {
		a.st.Delete(sid)
	}
	redirect(w, r, "/import", "msg", "Импорт отменён, база данных не изменена")
}

func (a *App) importCommit(w http.ResponseWriter, r *http.Request) {
	sid := a.sid(w, r, false)
	st := a.st.Get(sid)
	if st == nil {
		redirect(w, r, "/import", "err", "Нет загруженной выписки — загрузите файл заново")
		return
	}
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/import", "err", err.Error())
		return
	}
	var chosen []Operation
	seen := map[int]bool{}
	for _, v := range r.Form["sel"] {
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 || i >= len(st.Ops) || seen[i] {
			continue
		}
		seen[i] = true
		o := st.Ops[i].Operation
		if cat := strings.TrimSpace(r.FormValue("cat-" + v)); cat != "" && cat != o.Category {
			o.Category, o.Manual = cat, true
		}
		if _, sent := r.Form["ls-"+v]; sent && o.Direction == DirIn {
			if ls := normLS(strings.TrimSpace(r.FormValue("ls-" + v))); ls != o.LS {
				o.LS, o.Manual = ls, true
			}
		}
		chosen = append(chosen, o)
	}
	if len(chosen) == 0 {
		redirect(w, r, "/import", "err", "Не выбрано ни одной операции")
		return
	}
	rec, err := a.db.AddImport(ImportRecord{At: time.Now(), File: st.File, Account: st.Account, Org: st.Org,
		From: st.From, To: st.To, Status: "обработана"}, chosen)
	if err != nil {
		redirect(w, r, "/import", "err", "Ошибка сохранения: "+err.Error())
		return
	}
	a.st.Delete(sid)
	msg := fmt.Sprintf("Импортировано операций: %d", rec.Total)
	if rec.Skipped > 0 {
		msg += fmt.Sprintf(" (пропущено дублей: %d)", rec.Skipped)
	}
	redirect(w, r, "/", "msg", msg)
}

// ---- правка операции ----

type EditPage struct {
	Base
	Op      Operation
	Next    string
	Cats    []string
	AllCats []string
}

func safeNext(s string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.Contains(s, "\\") {
		return s
	}
	return "/operations"
}

func (a *App) editPage(w http.ResponseWriter, r *http.Request) {
	op, ok := a.db.Get(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	cats := OutCategories
	if op.Direction == DirIn {
		cats = InCategories
	}
	a.render(w, "edit", EditPage{Base: newBase("operations", "Изменение операции", r), Op: op,
		Next: safeNext(r.URL.Query().Get("next")), Cats: cats})
}

func (a *App) editSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.FormValue("id")
	op, ok := a.db.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	next := safeNext(r.FormValue("next"))
	cat := strings.TrimSpace(r.FormValue("category"))
	if cat == "" {
		cat = op.Category
	}
	ls := ""
	if op.Direction == DirIn {
		raw := strings.TrimSpace(r.FormValue("ls"))
		if raw != "" {
			if ls = normLS(raw); ls == "" {
				redirect(w, r, "/edit", "id", id, "next", next, "err", "Лицевой счёт — число от 1 до 999")
				return
			}
		}
	}
	period := ""
	if p := strings.TrimSpace(r.FormValue("period")); p != "" {
		if _, err := time.Parse("2006-01", p); err != nil {
			redirect(w, r, "/edit", "id", id, "next", next, "err", "Период укажите в формате «месяц-год»")
			return
		}
		period = p
	}
	if err := a.db.Update(id, cat, ls, period); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
