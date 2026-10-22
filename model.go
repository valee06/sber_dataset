package main

import "time"

// Направление операции по счёту.
const (
	DirIn  = "in"  // поступление (приход)
	DirOut = "out" // списание (расход)
)

// Operation — одна операция по расчётному счёту (платёжное поручение).
type Operation struct {
	ID           string `json:"id"`
	Date         string `json:"date"` // дата проводки, 2006-01-02
	Time         string `json:"time"` // время исполнения, 15:04:05 (если есть)
	Direction    string `json:"direction"`
	DocNo        string `json:"doc_no"`
	AmountKop    int64  `json:"amount_kop"` // сумма в копейках
	Counterparty string `json:"counterparty"`
	Via          string `json:"via,omitempty"` // банк/посредник (например, ПАО СБЕРБАНК)
	CounterINN   string `json:"counter_inn,omitempty"`
	CounterAcc   string `json:"counter_acc,omitempty"`
	KBK          string `json:"kbk,omitempty"`
	Purpose      string `json:"purpose"`
	PurposeRaw   string `json:"purpose_raw,omitempty"`
	Category     string `json:"category"`
	LS           string `json:"ls,omitempty"`     // лицевой счёт (номер квартиры)
	Period       string `json:"period,omitempty"` // период оплаты, 2006-01
	Manual       bool   `json:"manual,omitempty"` // правка вручную
	ImportID     string `json:"import_id,omitempty"`
}

// ImportRecord — запись журнала загрузок.
type ImportRecord struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	File     string    `json:"file"`
	Account  string    `json:"account"`
	Org      string    `json:"org"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Status   string    `json:"status"`
	Total    int       `json:"total"`
	InCount  int       `json:"in_count"`
	InSum    int64     `json:"in_sum"`
	OutCount int       `json:"out_count"`
	OutSum   int64     `json:"out_sum"`
	Skipped  int       `json:"skipped"`
}
