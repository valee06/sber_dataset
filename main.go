// Платежи по банковской выписке: импорт выписки СберБизнес (.xlsx),
// разбор платежей жильцов и операций по счёту. Только стандартная библиотека Go.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "адрес, на котором слушает сервер")
	dataDir := flag.String("data", "data", "каталог для хранения данных (БД и черновики импорта)")
	sample := flag.String("sample", "data/sample/statement_2018.xlsx", "приложенная выписка для кнопки «Разобрать приложенную выписку»")
	seed := flag.Bool("seed", false, "при пустой БД сразу импортировать приложенную выписку")
	reset := flag.Bool("reset", false, "очистить БД перед запуском")
	flag.Parse()

	if err := os.MkdirAll(filepath.Join(*dataDir, "sessions"), 0o755); err != nil {
		log.Fatal(err)
	}
	db, err := OpenDB(filepath.Join(*dataDir, "db.json"))
	if err != nil {
		log.Fatalf("не удалось открыть БД: %v", err)
	}
	if *reset {
		if err := db.Reset(); err != nil {
			log.Fatal(err)
		}
	}
	app := &App{db: db, st: NewStagings(filepath.Join(*dataDir, "sessions")), samplePath: *sample}
	app.st.Cleanup(30 * 24 * time.Hour)
	if err := app.loadTemplates(); err != nil {
		log.Fatal(err)
	}
	if *seed && len(db.Ops) == 0 {
		if err := app.seed(); err != nil {
			log.Fatalf("не удалось импортировать приложенную выписку: %v", err)
		}
		log.Printf("импортирована приложенная выписка: %d операций", len(db.Ops))
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("Сервер запущен: http://%s\n", *addr)
	log.Fatal(srv.ListenAndServe())
}

func (a *App) seed() error {
	ps, err := ParseStatement(a.samplePath, filepath.Base(a.samplePath))
	if err != nil {
		return err
	}
	_, err = a.db.AddImport(ImportRecord{At: time.Now(), File: ps.FileName, Account: ps.Account, Org: ps.Org,
		From: ps.From, To: ps.To, Status: "обработана"}, ps.Ops)
	return err
}
