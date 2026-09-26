package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var ErrInvalidWorkbook = errors.New("invalid XLSX workbook")

type cell struct {
	Ref     string `xml:"r,attr"`
	Type    string `xml:"t,attr"`
	Value   string `xml:"v"`
	Inline  string `xml:"is>t"`
	Formula string `xml:"f"`
}
type row struct {
	Number int    `xml:"r,attr"`
	Cells  []cell `xml:"c"`
}
type sheet struct {
	Rows []row `xml:"sheetData>row"`
}
type sharedString struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}
type sharedTable struct {
	Items []sharedString `xml:"si"`
}

func ReadFirstSheet(data []byte) ([][]string, error) {
	if len(data) > 5<<20 {
		return nil, ErrInvalidWorkbook
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrInvalidWorkbook
	}
	var stringsTable []string
	for _, file := range archive.File {
		if file.Name != "xl/sharedStrings.xml" {
			continue
		}
		body, err := readLimited(file, 10<<20)
		if err != nil {
			return nil, err
		}
		var table sharedTable
		if err := xml.Unmarshal(body, &table); err != nil {
			return nil, ErrInvalidWorkbook
		}
		for _, item := range table.Items {
			value := item.Text
			for _, run := range item.Runs {
				value += run.Text
			}
			stringsTable = append(stringsTable, value)
		}
	}
	for _, file := range archive.File {
		if file.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		body, err := readLimited(file, 10<<20)
		if err != nil {
			return nil, err
		}
		var page sheet
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, ErrInvalidWorkbook
		}
		if len(page.Rows) > 1001 {
			return nil, ErrInvalidWorkbook
		}
		rows := make([][]string, 0, len(page.Rows))
		for _, r := range page.Rows {
			values := make([]string, 6)
			for _, c := range r.Cells {
				if c.Formula != "" {
					return nil, fmt.Errorf("row %d contains a formula", r.Number)
				}
				column := columnIndex(c.Ref)
				if column < 0 || column >= len(values) {
					continue
				}
				value := c.Value
				if c.Type == "s" {
					index, err := strconv.Atoi(value)
					if err != nil || index < 0 || index >= len(stringsTable) {
						return nil, ErrInvalidWorkbook
					}
					value = stringsTable[index]
				}
				if c.Type == "inlineStr" {
					value = c.Inline
				}
				values[column] = strings.TrimSpace(value)
			}
			rows = append(rows, values)
		}
		return rows, nil
	}
	return nil, ErrInvalidWorkbook
}

func readLimited(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, ErrInvalidWorkbook
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrInvalidWorkbook
	}
	return body, nil
}

func columnIndex(ref string) int {
	value := 0
	found := false
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		value = value*26 + int(r-'A'+1)
		found = true
	}
	if !found {
		return -1
	}
	return value - 1
}
