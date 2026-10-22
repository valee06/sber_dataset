package main

// Минимальный потоковый читатель .xlsx на стандартной библиотеке Go
// (без внешних зависимостей): читает книгу, общие строки, стили (для
// распознавания дат) и листы.

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
)

// Cell — значение ячейки листа.
type Cell struct {
	S      string    // текст (для строковых ячеек)
	N      float64   // число
	IsNum  bool      // ячейка числовая
	IsDate bool      // числовая ячейка с форматом даты/времени
	T      time.Time // значение даты, если IsDate
}

// Row — непустая строка листа; ключ карты — индекс столбца (A = 0).
type Row struct {
	N     int
	Cells map[int]Cell
}

// Sheet — лист книги (только непустые строки и ячейки).
type Sheet struct {
	Name string
	Rows []Row
	idx  map[int]int
}

// Get возвращает ячейку (строка r — номер как в Excel, столбец c — с нуля).
func (s *Sheet) Get(r, c int) (Cell, bool) {
	i, ok := s.idx[r]
	if !ok {
		return Cell{}, false
	}
	cell, ok := s.Rows[i].Cells[c]
	return cell, ok
}

// Str возвращает текст ячейки без пробелов по краям ("" если ячейки нет).
func (s *Sheet) Str(r, c int) string {
	cell, ok := s.Get(r, c)
	if !ok {
		return ""
	}
	return cell.Text()
}

// Text — текстовое представление значения ячейки.
func (c Cell) Text() string {
	switch {
	case c.IsDate:
		return c.T.Format("2006-01-02 15:04:05")
	case c.IsNum:
		return strconv.FormatFloat(c.N, 'f', -1, 64)
	}
	return strings.TrimSpace(c.S)
}

// Workbook — открытая книга Excel.
type Workbook struct {
	zr     *zip.ReadCloser
	files  map[string]*zip.File
	Names  []string
	paths  []string
	shared []string
	dateXF []bool
}

type xmlWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}

type xmlRels struct {
	Rel []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

type xmlStyles struct {
	NumFmts []struct {
		ID   int    `xml:"numFmtId,attr"`
		Code string `xml:"formatCode,attr"`
	} `xml:"numFmts>numFmt"`
	Xfs []struct {
		NumFmtID int `xml:"numFmtId,attr"`
	} `xml:"cellXfs>xf"`
}

// OpenWorkbook открывает .xlsx-файл.
func OpenWorkbook(filename string) (*Workbook, error) {
	zr, err := zip.OpenReader(filename)
	if err != nil {
		return nil, fmt.Errorf("файл не является корректным .xlsx: %w", err)
	}
	w := &Workbook{zr: zr, files: map[string]*zip.File{}}
	for _, f := range zr.File {
		w.files[f.Name] = f
	}
	if err := w.load(); err != nil {
		zr.Close()
		return nil, err
	}
	return w, nil
}

// Close закрывает книгу.
func (w *Workbook) Close() { w.zr.Close() }

func (w *Workbook) decodeXML(name string, v any) error {
	f, ok := w.files[name]
	if !ok {
		return fmt.Errorf("в книге нет части %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return xml.NewDecoder(rc).Decode(v)
}

func (w *Workbook) load() error {
	var wb xmlWorkbook
	if err := w.decodeXML("xl/workbook.xml", &wb); err != nil {
		return err
	}
	var rels xmlRels
	if err := w.decodeXML("xl/_rels/workbook.xml.rels", &rels); err != nil {
		return err
	}
	target := map[string]string{}
	for _, r := range rels.Rel {
		t := r.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Join("xl", t)
		}
		target[r.ID] = t
	}
	for _, s := range wb.Sheets {
		w.Names = append(w.Names, s.Name)
		w.paths = append(w.paths, target[s.RID])
	}
	if f, ok := w.files["xl/sharedStrings.xml"]; ok {
		ss, err := readShared(f)
		if err != nil {
			return err
		}
		w.shared = ss
	}
	var st xmlStyles
	if _, ok := w.files["xl/styles.xml"]; ok {
		if err := w.decodeXML("xl/styles.xml", &st); err != nil {
			return err
		}
		custom := map[int]string{}
		for _, nf := range st.NumFmts {
			custom[nf.ID] = nf.Code
		}
		for _, xf := range st.Xfs {
			w.dateXF = append(w.dateXF, isDateFormat(xf.NumFmtID, custom[xf.NumFmtID]))
		}
	}
	return nil
}

func isDateFormat(id int, code string) bool {
	switch {
	case id >= 14 && id <= 22, id >= 27 && id <= 36, id >= 45 && id <= 47, id >= 50 && id <= 58:
		return true
	}
	if code == "" {
		return false
	}
	// убираем кавычки, экранированные символы и [цвета/локали]
	var b strings.Builder
	inQuote, inBracket := false, false
	rs := []rune(code)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '"':
			inQuote = !inQuote
		case inQuote:
		case r == '[':
			inBracket = true
		case r == ']':
			inBracket = false
		case inBracket:
		case r == '\\' || r == '_' || r == '*':
			i++
		default:
			b.WriteRune(r)
		}
	}
	return strings.ContainsAny(strings.ToLower(b.String()), "dmyhs")
}

func readShared(f *zip.File) ([]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	d := xml.NewDecoder(bufio.NewReaderSize(rc, 1<<20))
	var out []string
	var sb strings.Builder
	inSI, inT := false, false
	skip := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				sb.Reset()
			case "rPh":
				skip++
			case "t":
				if inSI && skip == 0 {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, sb.String())
				inSI = false
			case "rPh":
				skip--
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				sb.Write(t)
			}
		}
	}
	return out, nil
}

func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			n = n*26 + int(r-'A') + 1
		} else if r >= 'a' && r <= 'z' {
			n = n*26 + int(r-'a') + 1
		} else {
			break
		}
	}
	return n - 1
}

func excelTime(f float64) time.Time {
	days := math.Floor(f)
	secs := int(math.Round((f - days) * 86400))
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, int(days)).Add(time.Duration(secs) * time.Second)
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// ReadSheet читает лист по номеру (с нуля).
func (w *Workbook) ReadSheet(i int) (*Sheet, error) {
	if i < 0 || i >= len(w.paths) {
		return nil, fmt.Errorf("нет листа %d", i)
	}
	f, ok := w.files[w.paths[i]]
	if !ok {
		return nil, fmt.Errorf("нет файла листа %s", w.paths[i])
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	sh := &Sheet{Name: w.Names[i], idx: map[int]int{}}
	d := xml.NewDecoder(bufio.NewReaderSize(rc, 1<<20))

	var cur *Row
	lastRow := 0
	var col, style int
	var ctype string
	var vbuf strings.Builder
	var inV, inIS, inT, hasVal bool

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				n, err := strconv.Atoi(attr(t, "r"))
				if err != nil {
					n = lastRow + 1
				}
				lastRow = n
				cur = &Row{N: n, Cells: map[int]Cell{}}
			case "c":
				col = colIndex(attr(t, "r"))
				style, _ = strconv.Atoi(attr(t, "s"))
				ctype = attr(t, "t")
				vbuf.Reset()
				hasVal = false
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				if inIS {
					inT = true
				}
			}
		case xml.CharData:
			if inV || inT {
				vbuf.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
				hasVal = true
			case "t":
				if inT {
					inT = false
					hasVal = true
				}
			case "is":
				inIS = false
			case "c":
				if !hasVal || cur == nil {
					continue
				}
				raw := vbuf.String()
				var cell Cell
				switch ctype {
				case "s":
					idx, err := strconv.Atoi(strings.TrimSpace(raw))
					if err != nil || idx < 0 || idx >= len(w.shared) {
						continue
					}
					cell.S = w.shared[idx]
				case "str", "inlineStr", "b":
					cell.S = raw
				case "e":
					continue
				default:
					fv, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
					if err != nil {
						continue
					}
					if style >= 0 && style < len(w.dateXF) && w.dateXF[style] {
						cell.IsDate = true
						cell.T = excelTime(fv)
					} else {
						cell.IsNum = true
					}
					cell.N = fv
				}
				if !cell.IsNum && !cell.IsDate && strings.TrimSpace(cell.S) == "" {
					continue
				}
				cur.Cells[col] = cell
			case "row":
				if cur != nil && len(cur.Cells) > 0 {
					sh.idx[cur.N] = len(sh.Rows)
					sh.Rows = append(sh.Rows, *cur)
				}
				cur = nil
			}
		}
	}
	return sh, nil
}
