package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Категории операций.
const (
	CatRent      = "Квартплата"
	CatSubsidy   = "Субсидии"
	CatLease     = "Аренда"
	CatInOther   = "Прочие поступления"
	CatTax       = "Налоги и взносы"
	CatFine      = "Штрафы"
	CatSalary    = "Зарплата"
	CatUtilities = "Коммунальные ресурсы"
	CatCapRepair = "Капремонт"
	CatTSN       = "Передача средств ТСН"
	CatBank      = "Банковские услуги"
	CatAccount   = "Подотчёт"
	CatServices  = "Услуги и подрядчики"
)

// InCategories и OutCategories — списки категорий для выпадающих списков.
var InCategories = []string{CatRent, CatSubsidy, CatLease, CatInOther}
var OutCategories = []string{CatTax, CatFine, CatSalary, CatUtilities, CatCapRepair, CatTSN, CatBank, CatAccount, CatServices}

// AllCategories — все известные категории.
func AllCategories() []string {
	return append(append([]string{}, InCategories...), OutCategories...)
}

const wordStart = `(?:^|[^а-яёa-z])`

var (
	reLS = []*regexp.Regexp{
		// «л/с 56», «лс 0000000134», «л с 164», «ЛСЧ 115», «л.с 187», «лицевой счёт 12»
		regexp.MustCompile(wordStart + `(?:л\.?\s*/?\s*с(?:ч|чет)?\.?|лиц[а-яё]*\s*сч[а-яё]*)\s*[№#:.]?\s*0*(\d{1,3})(?:\D|$)`),
		// «кв 161», «кв.76», «квартира 161», «кв номер 142»
		regexp.MustCompile(wordStart + `кв(?:артир[а-яё]*|арт\.?|\.)?\s*(?:№|номер)?\s*[.:]?\s*0*(\d{1,3})(?:\D|$)`),
		// адрес «12-2-37», «12/2-142»
		regexp.MustCompile(`12\s*[-/]\s*2\s*[-/]\s*(\d{1,3})(?:\D|$)`),
		// «л 52 за 01 2018»
		regexp.MustCompile(wordStart + `л\s+0*(\d{1,3})(?:\D|$)`),
	}
	reYear = regexp.MustCompile(`^20\d\d$`)

	reMonthName = regexp.MustCompile(wordStart + `(январ|феврал|март|апрел|ма[йяе]|июн|июл|август|сентябр|октябр|ноябр|декабр)[а-яё]*`)
	reYearAfter = regexp.MustCompile(`^[^0-9]{0,14}(20\d\d)`)
	reMonthNum  = regexp.MustCompile(`(?:^|[^\d./\-])(\d{1,2})\s*[,./\- ]\s*(20\d\d)(?:\D|$)`)

	reRentWords = regexp.MustCompile(`квартпл|кварплат|кв\.?\s*плат|жку|жилищн|коммунал|за отопл|электроэн|водоснабж|л/с|лс|взнос`)
	reSalary    = regexp.MustCompile(`зарплат|заработн|аванс|на выдачу|премии|премия`)
	reUtilities = regexp.MustCompile(`э/э|электроэн|водоснабж|за воду|отоплен|мосводоканал|энергосбыт|моэк|теплоснабж`)
)

var monthStems = map[string]int{
	"январ": 1, "феврал": 2, "март": 3, "апрел": 4, "мая": 5, "май": 5, "мае": 5,
	"июн": 6, "июл": 7, "август": 8, "сентябр": 9, "октябр": 10, "ноябр": 11, "декабр": 12,
}

// extractLS определяет лицевой счёт (номер квартиры) по назначению платежа и
// реквизитам плательщика. Возвращает "" если определить не удалось.
func extractLS(purpose, payerRaw string) string {
	p := strings.ToLower(purpose)
	for _, re := range reLS {
		if m := re.FindStringSubmatch(p); m != nil {
			return normLS(m[1])
		}
	}
	// в назначении только цифры: «12 2017 124», «079 12 2017», «185»
	if ls := numericOnlyLS(p); ls != "" {
		return ls
	}
	// запасной вариант — адрес плательщика
	a := strings.ToLower(payerRaw)
	for _, re := range reLS[1:3] {
		if m := re.FindStringSubmatch(a); m != nil {
			return normLS(m[1])
		}
	}
	return ""
}

func normLS(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func numericOnlyLS(p string) string {
	fields := strings.FieldsFunc(p, func(r rune) bool { return r == ' ' || r == ',' || r == '.' || r == '/' || r == '-' })
	if len(fields) == 0 || len(fields) > 5 {
		return ""
	}
	for _, f := range fields {
		for _, r := range f {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	var rest []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if reYear.MatchString(f) {
			// убираем год и предшествующий ему номер месяца
			if n := len(rest); n > 0 {
				if m, _ := strconv.Atoi(rest[n-1]); m >= 1 && m <= 12 && len(rest[n-1]) <= 2 {
					rest = rest[:n-1]
				}
			}
			continue
		}
		rest = append(rest, f)
	}
	if len(rest) != 1 || len(rest[0]) > 3 {
		return ""
	}
	return normLS(rest[0])
}

// extractPeriod определяет период оплаты (месяц) вида «2018-03».
// opDate — дата операции (2006-01-02), нужна если год в тексте не указан.
func extractPeriod(purpose, opDate string) string {
	p := strings.ToLower(purpose)
	if loc := reMonthName.FindStringSubmatchIndex(p); loc != nil {
		stem := p[loc[2]:loc[3]]
		month := 0
		for k, v := range monthStems {
			if strings.HasPrefix(stem, k) {
				month = v
			}
		}
		if month > 0 {
			rest := p[loc[1]:]
			if m := reYearAfter.FindStringSubmatch(rest); m != nil {
				return m[1] + "-" + pad2(month)
			}
			if len(opDate) >= 7 {
				y, _ := strconv.Atoi(opDate[:4])
				om, _ := strconv.Atoi(opDate[5:7])
				if month > om+1 {
					y--
				}
				return strconv.Itoa(y) + "-" + pad2(month)
			}
		}
	}
	if m := reMonthNum.FindStringSubmatch(p); m != nil {
		mo, _ := strconv.Atoi(m[1])
		if mo >= 1 && mo <= 12 {
			return m[2] + "-" + pad2(mo)
		}
	}
	return ""
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// classify назначает категорию операции по эвристикам.
func classify(op *Operation, payerRaw string) string {
	t := strings.ToLower(op.Purpose)
	cp := strings.ToLower(op.Counterparty)
	if op.Direction == DirIn {
		switch {
		case strings.Contains(t, "субс") || strings.Contains(cp, "департамент финансов"):
			return CatSubsidy
		case strings.Contains(t, "аренд") || strings.Contains(t, "коммунальных платежей и прочих услуг"):
			return CatLease
		case op.LS != "" || reRentWords.MatchString(t) || strings.HasPrefix(op.CounterAcc, "30233"):
			return CatRent
		}
		return CatInOther
	}
	switch {
	case strings.Contains(cp, "сбербанк") && strings.Contains(t, "комисси"):
		return CatBank
	case op.KBK != "":
		if strings.Contains(t, "штраф") || strings.HasPrefix(op.KBK, "392") && strings.Contains(t, "штраф") {
			return CatFine
		}
		return CatTax
	case strings.Contains(t, "штраф"):
		return CatFine
	case strings.Contains(cp, "тсн") || strings.Contains(cp, "тсж") || strings.Contains(t, "передача средств"):
		return CatTSN
	case strings.Contains(t, "кап. ремонт") || strings.Contains(t, "кап.ремонт") || strings.Contains(t, "капремонт") || strings.Contains(cp, "фкр"):
		return CatCapRepair
	case strings.Contains(t, "подотчет") || strings.Contains(t, "подотчёт"):
		return CatAccount
	case reSalary.MatchString(t):
		return CatSalary
	case reUtilities.MatchString(t) || reUtilities.MatchString(cp):
		return CatUtilities
	}
	return CatServices
}

// sortedLS сортирует номера лицевых счетов по возрастанию как числа.
func sortedLS(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a < b
	})
	return out
}
