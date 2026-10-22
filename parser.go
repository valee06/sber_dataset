package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ParsedStatement — результат разбора выписки.
type ParsedStatement struct {
	FileName string
	Account  string
	Org      string
	From, To string // период выписки, 2006-01-02
	Ops      []Operation
	Skipped  int
	Warnings []string
}

type rawOrder struct {
	DocNo               string
	Date, Time          string
	Amount              string
	PayerName, PayerAcc string
	PayerINN            string
	RecvName, RecvAcc   string
	RecvINN             string
	KBK, Purpose        string
}

var (
	reAcc20  = regexp.MustCompile(`^\d{20}$`)
	reRuDate = regexp.MustCompile(`(\d{1,2})\s+([а-яё]+)\s+(\d{4})`)
	ruMonths = map[string]time.Month{"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6, "июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12}
	reSpaces = regexp.MustCompile(`\s+`)
)

const (
	colText = 1  // B — основной текст блока платёжного поручения
	colVal  = 29 // AD — значения (сумма, счета, БИК)
	colExec = 27 // AB — дата/время исполнения банком
)

// ParseStatement разбирает выписку СберБизнес (.xlsx): лист с платёжными
// поручениями и заголовок выписки (счёт, организация, период).
func ParseStatement(filename, displayName string) (*ParsedStatement, error) {
	wb, err := OpenWorkbook(filename)
	if err != nil {
		return nil, err
	}
	defer wb.Close()

	ps := &ParsedStatement{FileName: displayName}
	var orders []rawOrder
	for i := range wb.Names {
		sh, err := wb.ReadSheet(i)
		if err != nil {
			return nil, fmt.Errorf("лист «%s»: %w", wb.Names[i], err)
		}
		parseHeader(sh, ps)
		o, warns := parseOrders(sh)
		orders = append(orders, o...)
		ps.Warnings = append(ps.Warnings, warns...)
	}
	if len(orders) == 0 {
		return nil, fmt.Errorf("в файле не найдены платёжные поручения: ожидается выписка СберБизнес с листом «operations»")
	}
	if ps.Account == "" {
		ps.Account = guessAccount(orders)
	}

	seen := map[string]int{}
	for _, o := range orders {
		op, ok := o.toOperation(ps.Account)
		if !ok {
			ps.Skipped++
			continue
		}
		seen[op.ID]++
		if n := seen[op.ID]; n > 1 {
			op.ID = fmt.Sprintf("%s-%d", op.ID, n)
		}
		ps.Ops = append(ps.Ops, op)
	}
	sort.SliceStable(ps.Ops, func(i, j int) bool {
		a, b := ps.Ops[i], ps.Ops[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		return a.Time < b.Time
	})
	if ps.From == "" && len(ps.Ops) > 0 {
		ps.From, ps.To = ps.Ops[0].Date, ps.Ops[len(ps.Ops)-1].Date
	}
	return ps, nil
}

// parseHeader достаёт из шапки выписки номер счёта, организацию и период.
func parseHeader(sh *Sheet, ps *ParsedStatement) {
	limit := 30
	for k, row := range sh.Rows {
		if k >= limit {
			break
		}
		var texts []string
		cols := sortedCols(row)
		for _, c := range cols {
			if t := row.Cells[c].Text(); t != "" {
				texts = append(texts, t)
			}
		}
		joined := strings.Join(texts, " ")
		upper := strings.ToUpper(joined)
		if strings.Contains(upper, "ВЫПИСКА ОПЕРАЦИЙ") {
			// счёт и организация — в следующих строках
			for _, nr := range sh.Rows[k+1 : minInt(k+4, len(sh.Rows))] {
				for _, c := range sortedCols(nr) {
					t := nr.Cells[c].Text()
					switch {
					case ps.Account == "" && reAcc20.MatchString(t):
						ps.Account = t
					case ps.Org == "" && !reAcc20.MatchString(t) && !strings.Contains(strings.ToLower(t), "за период") && len(t) > 3:
						ps.Org = t
					}
				}
			}
			for _, c := range sortedCols(row) {
				if t := row.Cells[c].Text(); reAcc20.MatchString(t) && ps.Account == "" {
					ps.Account = t
				}
			}
		}
		if strings.Contains(strings.ToLower(joined), "за период с") {
			var ds []string
			for _, m := range reRuDate.FindAllStringSubmatch(strings.ToLower(joined), -1) {
				if mo, ok := ruMonths[m[2]]; ok {
					d, _ := strconv.Atoi(m[1])
					y, _ := strconv.Atoi(m[3])
					ds = append(ds, time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
				}
			}
			if len(ds) >= 2 {
				ps.From, ps.To = ds[0], ds[1]
			}
		}
	}
}

func sortedCols(r Row) []int {
	cols := make([]int, 0, len(r.Cells))
	for c := range r.Cells {
		cols = append(cols, c)
	}
	sort.Ints(cols)
	return cols
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// valueAfter возвращает первое значение справа от ячейки с подписью label.
func valueAfter(sh *Sheet, rowN int, label string) string {
	i, ok := sh.idx[rowN]
	if !ok {
		return ""
	}
	row := sh.Rows[i]
	cols := sortedCols(row)
	found := false
	for _, c := range cols {
		t := row.Cells[c].Text()
		if found && t != "" {
			return t
		}
		if t == label {
			found = true
		}
	}
	return ""
}

func firstText(sh *Sheet, rowN int) string {
	i, ok := sh.idx[rowN]
	if !ok {
		return ""
	}
	row := sh.Rows[i]
	for _, c := range sortedCols(row) {
		if t := row.Cells[c].Text(); t != "" {
			return t
		}
	}
	return ""
}

func findLabel(sh *Sheet, from, to int, label string) int {
	for r := from; r <= to; r++ {
		if sh.Str(r, colText) == label {
			return r
		}
	}
	return 0
}

func parseOrders(sh *Sheet) ([]rawOrder, []string) {
	var titles []int
	for _, row := range sh.Rows {
		if c, ok := row.Cells[colText]; ok && strings.HasPrefix(strings.ToUpper(c.Text()), "ПЛАТЕЖНОЕ ПОРУЧЕНИЕ") {
			titles = append(titles, row.N)
		}
	}
	var out []rawOrder
	var warns []string
	for k, n := range titles {
		lim := n + 40
		if k+1 < len(titles) {
			lim = minInt(lim, titles[k+1]-7)
		}
		pl := findLabel(sh, n, lim, "Плательщик")
		bp := findLabel(sh, n, lim, "Банк Получателя")
		rl := findLabel(sh, n, lim, "Получатель")
		nl := findLabel(sh, n, lim, "Назначение платежа")
		if pl == 0 || bp == 0 || rl == 0 || nl == 0 {
			warns = append(warns, fmt.Sprintf("строка %d: не удалось разобрать структуру платёжного поручения", n))
			continue
		}
		o := rawOrder{}
		if i := strings.Index(sh.Str(n, colText), "№"); i >= 0 {
			o.DocNo = strings.TrimSpace(sh.Str(n, colText)[i+len("№"):])
		}
		o.Amount = valueAfter(sh, pl-3, "Сумма")
		o.PayerINN = valueAfter(sh, pl-3, "ИНН")
		o.PayerName = firstText(sh, pl-2)
		o.PayerAcc = valueAfter(sh, pl-1, "Сч. №")
		o.RecvINN = valueAfter(sh, bp+1, "ИНН")
		o.RecvAcc = valueAfter(sh, bp+1, "Сч. №")
		o.RecvName = firstText(sh, bp+2)
		var purpose []string
		for r := rl + 1; r < nl; r++ {
			t := sh.Str(r, colText)
			if t == "" {
				continue
			}
			if reAcc20.MatchString(t) {
				o.KBK = t
				continue
			}
			purpose = append(purpose, t)
		}
		o.Purpose = strings.Join(purpose, " ")
		for r := n - 1; r >= maxInt(n-10, 1); r-- {
			if c, ok := sh.Get(r, colText); ok && c.IsDate {
				o.Date = c.T.Format("2006-01-02")
				break
			}
		}
		for r := n + 15; r <= minInt(n+40, lim+7); r++ {
			if c, ok := sh.Get(r, colExec); ok && c.IsDate {
				o.Time = c.T.Format("15:04:05")
				if o.Date == "" {
					o.Date = c.T.Format("2006-01-02")
				}
				break
			}
		}
		if o.Date == "" || o.Amount == "" {
			warns = append(warns, fmt.Sprintf("платёжное поручение № %s: нет даты или суммы", o.DocNo))
			continue
		}
		out = append(out, o)
	}
	return out, warns
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// guessAccount: если в шапке счёта нет — берём номер, встречающийся чаще всего.
func guessAccount(orders []rawOrder) string {
	cnt := map[string]int{}
	for _, o := range orders {
		if reAcc20.MatchString(o.PayerAcc) {
			cnt[o.PayerAcc]++
		}
		if reAcc20.MatchString(o.RecvAcc) {
			cnt[o.RecvAcc]++
		}
	}
	best, bn := "", 0
	for a, n := range cnt {
		if n > bn {
			best, bn = a, n
		}
	}
	return best
}

// parseKop разбирает сумму «3500-00», «311-27», «3500.50», «3500» в копейки.
func parseKop(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", ""))
	s = strings.NewReplacer("-", ".", ",", ".").Replace(s)
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, false
	}
	rub, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	var kop int64
	if len(parts) == 2 {
		f := (parts[1] + "00")[:2]
		kop, err = strconv.ParseInt(f, 10, 64)
		if err != nil {
			return 0, false
		}
	}
	return rub*100 + kop, true
}

func oneLine(s string) string {
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// cleanName выделяет имя контрагента из строки вида
// «ПАО СБЕРБАНК//ИВАНОВ ИВАН//1234//адрес//». Второе значение — банк-посредник.
func cleanName(raw string) (name, via string) {
	raw = oneLine(raw)
	if strings.Contains(raw, "//") {
		parts := strings.Split(raw, "//")
		first := strings.TrimSpace(parts[0])
		if strings.Contains(strings.ToUpper(first), "БАНК") && len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			return strings.TrimPrefix(strings.TrimSpace(parts[1]), "БД "), first
		}
		if first != "" {
			return first, ""
		}
	}
	if i := strings.Index(raw, " / "); i > 0 && strings.Contains(strings.ToUpper(raw[:i]), "БАНК") {
		return strings.TrimSpace(raw[i+3:]), strings.TrimSpace(raw[:i])
	}
	if i := strings.Index(raw, " р/с "); i > 0 {
		return strings.TrimSpace(raw[:i]), ""
	}
	return raw, ""
}

func cleanPurpose(raw string) string {
	raw = oneLine(raw)
	const marker = "Назначение платежа:"
	if i := strings.Index(raw, marker); i >= 0 {
		raw = strings.TrimSpace(raw[i+len(marker):])
	}
	return raw
}

func (o rawOrder) toOperation(ourAcc string) (Operation, bool) {
	kop, ok := parseKop(o.Amount)
	if !ok {
		return Operation{}, false
	}
	op := Operation{Date: o.Date, Time: o.Time, DocNo: o.DocNo, AmountKop: kop, KBK: o.KBK,
		PurposeRaw: oneLine(o.Purpose), Purpose: cleanPurpose(o.Purpose)}
	var counterRaw string
	switch {
	case o.RecvAcc == ourAcc:
		op.Direction = DirIn
		counterRaw, op.CounterINN, op.CounterAcc = o.PayerName, o.PayerINN, o.PayerAcc
	case o.PayerAcc == ourAcc:
		op.Direction = DirOut
		counterRaw, op.CounterINN, op.CounterAcc = o.RecvName, o.RecvINN, o.RecvAcc
	default:
		return Operation{}, false
	}
	if op.CounterINN == "0" {
		op.CounterINN = ""
	}
	op.Counterparty, op.Via = cleanName(counterRaw)
	if op.Direction == DirIn {
		op.LS = extractLS(op.Purpose, counterRaw)
	}
	op.Period = extractPeriod(op.Purpose, op.Date)
	op.Category = classify(&op, counterRaw)
	if op.Category != CatRent && op.Direction == DirIn {
		op.LS = ""
	}
	h := sha1.Sum(fmt.Appendf(nil, "%s|%s|%d|%s|%s|%s|%s", op.Date, op.DocNo, op.AmountKop, op.Direction, op.CounterAcc, op.CounterINN, op.PurposeRaw))
	op.ID = hex.EncodeToString(h[:6])
	return op, true
}
