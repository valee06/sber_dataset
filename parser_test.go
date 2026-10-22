package main

import (
	"os"
	"testing"
)

func TestExtractLS(t *testing.T) {
	cases := []struct{ purpose, payer, want string }{
		{"л с 164", "", "164"},
		{"квартплата зааянварь 2018", "", ""},
		{"Квартплата по адресу улица Осенний бульвар 12,корпус 2,квартира 161", "", "161"},
		{"ОСЕННИЙ Б-Р 12-2-24 НОЯБРЬ 2017", "", "24"},
		{"ЛС 116 12,2017", "", "116"},
		{"КОММУНАЛЬНЫЕ УСЛУГИ ПО Л/С 56,АСТАШКИНА Е.А.,ОСЕННИЙ Б-Р,Д.12-2-56;ИПГУ:;", "", "56"},
		{"12 2017 124", "", "124"},
		{"079 12 2017", "", "79"},
		{"185", "", "185"},
		{"12 2017", "", ""},
		{"Перевод с карты *6832, Кв.плата за текущий месяц 2017г., л/с 0000000134, Иванова И.И..", "", "134"},
		{"ЛС 00000000006 ДЕКАБРЬ 2017", "", "6"},
		{"квартплата", "ПАО СБЕРБАНК//ИВАНОВ//1//г москва осенний б-р 12-2-4//", "4"},
		{"(02010030420052500631242, л/сч 02732592000):78531.61 Субс.", "", ""},
		{"Оплата за аренду за январь 2018 г по договору", "", ""},
		{"за 1 кв. 2017", "", ""},
	}
	for _, c := range cases {
		if got := extractLS(c.purpose, c.payer); got != c.want {
			t.Errorf("extractLS(%q,%q) = %q, want %q", c.purpose, c.payer, got, c.want)
		}
	}
}

func TestExtractPeriod(t *testing.T) {
	cases := []struct{ purpose, date, want string }{
		{"Зарплата за март 2018. НДС не облагается.", "2018-04-10", "2018-03"},
		{"ЖКУ ЗА 12-2017, В ТОМ ЧИСЛЕ НДС", "2018-01-10", "2017-12"},
		{"ЖКУ 01/2018 ЛСЧ 141", "2018-02-17", "2018-01"},
		{"ЗА 15/03/2018 КУРЕНКОВ", "2018-03-16", ""},
		{"Оплата по счету от 30.12.2017", "2018-01-16", ""},
		{"Передача средств, полученных в июне - августе 2018 г.", "2018-09-01", "2018-06"},
		{"квартплата за январь", "2018-02-05", "2018-01"},
	}
	for _, c := range cases {
		if got := extractPeriod(c.purpose, c.date); got != c.want {
			t.Errorf("extractPeriod(%q) = %q, want %q", c.purpose, got, c.want)
		}
	}
}

func TestParseKop(t *testing.T) {
	for in, want := range map[string]int64{"3500-00": 350000, "311-27": 31127, "3500.5": 350050, "12": 1200} {
		if got, ok := parseKop(in); !ok || got != want {
			t.Errorf("parseKop(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
}

// Проверка на реальной выписке (пропускается, если файла нет).
func TestParseSampleStatement(t *testing.T) {
	const file = "data/sample/statement_2018.xlsx"
	if _, err := os.Stat(file); err != nil {
		t.Skip("нет примера выписки")
	}
	ps, err := ParseStatement(file, "sample.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	var inN, outN int
	var inS, outS int64
	for _, o := range ps.Ops {
		if o.Direction == DirIn {
			inN++
			inS += o.AmountKop
		} else {
			outN++
			outS += o.AmountKop
		}
	}
	if inN != 413 || inS != 347173316 || outN != 140 || outS != 542422925 {
		t.Fatalf("итоги: приход %d/%d, расход %d/%d", inN, inS, outN, outS)
	}
	if ps.Account != "40703810838190100208" || ps.From != "2018-01-01" || ps.To != "2018-12-31" {
		t.Fatalf("шапка: %q %q %q", ps.Account, ps.From, ps.To)
	}
}
