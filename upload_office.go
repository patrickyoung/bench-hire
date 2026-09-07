package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Office containers are read in memory; archive paths are never extracted to
// disk. Declared and actual expanded sizes are bounded before parsing XML.
func extractOffice(_ string, raw []byte) ([]byte, string, string, error) {
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(z.File) > 2000 {
		return nil, "", "", errors.New("this Office file is unreadable or has too many parts")
	}
	parts := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		if f.UncompressedSize64 > 32<<20 || total > (32<<20)-f.UncompressedSize64 {
			return nil, "", "", errors.New("this Office file expands beyond 32 MiB; split or export it as text")
		}
		total += f.UncompressedSize64
		if parts[f.Name] != nil {
			return nil, "", "", errors.New("Office file contains duplicate parts")
		}
		parts[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f := parts[name]
		if f == nil {
			return nil, fmt.Errorf("missing Office part %s", name)
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		data, err := io.ReadAll(io.LimitReader(r, (8<<20)+1))
		if err != nil || len(data) > 8<<20 {
			return nil, errors.New("an Office document part exceeds 8 MiB or could not be read")
		}
		return data, nil
	}
	hasContent := false
	var out strings.Builder
	out.WriteString("Text and cell values extracted from the original. Embedded images, charts, layout, and macros are not interpreted. Consult the retained original for those details.\n\n")
	mediaType, method := "", ""
	switch {
	case parts["word/document.xml"] != nil:
		mediaType, method = "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx-text"
		for _, name := range sortedOfficeParts(parts, "word/", func(n string) bool {
			return n == "word/document.xml" || n == "word/footnotes.xml" || n == "word/endnotes.xml" || strings.HasPrefix(n, "word/header") || strings.HasPrefix(n, "word/footer")
		}) {
			data, err := read(name)
			if err != nil {
				return nil, "", "", err
			}
			text, err := officeText(data, "t")
			if err != nil {
				return nil, "", "", err
			}
			hasContent = hasContent || strings.TrimSpace(text) != ""
			if out.Len()+len(text)+len(name)+100 > uploadTextLimit-1024 {
				return nil, "", "", errors.New("extracted Office content exceeds 2 MiB; split the document")
			}
			fmt.Fprintf(&out, "## %s\n\n%s\n\n", path.Base(name), text)
		}
	case parts["xl/workbook.xml"] != nil:
		mediaType, method = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx-cells"
		var shared []string
		if parts["xl/sharedStrings.xml"] != nil {
			data, err := read("xl/sharedStrings.xml")
			if err != nil {
				return nil, "", "", err
			}
			var table struct {
				Items []struct {
					Text string `xml:"t"`
					Runs []struct {
						Text string `xml:"t"`
					} `xml:"r"`
				} `xml:"si"`
			}
			if err := xml.Unmarshal(data, &table); err != nil {
				return nil, "", "", err
			}
			for _, item := range table.Items {
				value := item.Text
				for _, run := range item.Runs {
					value += run.Text
				}
				shared = append(shared, value)
			}
		}
		data, err := read("xl/workbook.xml")
		if err != nil {
			return nil, "", "", err
		}
		var book struct {
			Sheets []struct {
				Name string `xml:"name,attr"`
				ID   string `xml:"id,attr"`
			} `xml:"sheets>sheet"`
		}
		if err := xml.Unmarshal(data, &book); err != nil {
			return nil, "", "", err
		}
		targets := map[string]string{}
		if parts["xl/_rels/workbook.xml.rels"] != nil {
			data, err := read("xl/_rels/workbook.xml.rels")
			if err != nil {
				return nil, "", "", err
			}
			var rels struct {
				Items []struct {
					ID     string `xml:"Id,attr"`
					Target string `xml:"Target,attr"`
					Mode   string `xml:"TargetMode,attr"`
				} `xml:"Relationship"`
			}
			if err := xml.Unmarshal(data, &rels); err != nil {
				return nil, "", "", err
			}
			for _, rel := range rels.Items {
				if rel.Mode != "External" {
					target := path.Join("xl", rel.Target)
					if strings.HasPrefix(rel.Target, "/") {
						target = strings.TrimPrefix(rel.Target, "/")
					}
					targets[rel.ID] = target
				}
			}
		}
		for i, sheet := range book.Sheets {
			target := targets[sheet.ID]
			if target == "" {
				target = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
			}
			data, err := read(target)
			if err != nil {
				return nil, "", "", err
			}
			var worksheet struct {
				Rows []struct {
					Cells []struct {
						Ref     string `xml:"r,attr"`
						Type    string `xml:"t,attr"`
						Value   string `xml:"v"`
						Formula string `xml:"f"`
						Inline  struct {
							Text string `xml:"t"`
							Runs []struct {
								Text string `xml:"t"`
							} `xml:"r"`
						} `xml:"is"`
					} `xml:"c"`
				} `xml:"sheetData>row"`
			}
			if err := xml.Unmarshal(data, &worksheet); err != nil {
				return nil, "", "", err
			}
			fmt.Fprintf(&out, "## Sheet: %s\n\nCell addresses preserve empty columns. Dates may be stored as spreadsheet serial numbers; formatting is in the original. Formula values are cached values, not recalculated.\n\n", sheet.Name)
			for _, row := range worksheet.Rows {
				for _, cell := range row.Cells {
					value := cell.Value
					if cell.Type == "s" {
						n, err := strconv.Atoi(value)
						if err != nil || n < 0 || n >= len(shared) {
							return nil, "", "", errors.New("spreadsheet contains an invalid shared text reference")
						}
						value = shared[n]
					}
					if cell.Type == "inlineStr" {
						value = cell.Inline.Text
						for _, run := range cell.Inline.Runs {
							value += run.Text
						}
					}
					if cell.Formula != "" {
						value += " (formula: " + cell.Formula + ")"
					}
					hasContent = hasContent || strings.TrimSpace(value) != ""
					if out.Len()+len(cell.Ref)+len(value)+100 > uploadTextLimit-1024 {
						return nil, "", "", errors.New("extracted spreadsheet content exceeds 2 MiB; split the workbook")
					}
					fmt.Fprintf(&out, "%s\t%s\n", cell.Ref, value)
				}
				out.WriteString("\n")
			}
		}
	case parts["ppt/presentation.xml"] != nil:
		mediaType, method = "application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx-text"
		for _, name := range sortedOfficeParts(parts, "ppt/slides/slide", func(n string) bool { return !strings.Contains(strings.TrimPrefix(n, "ppt/slides/"), "/") }) {
			data, err := read(name)
			if err != nil {
				return nil, "", "", err
			}
			text, err := officeText(data, "t")
			if err != nil {
				return nil, "", "", err
			}
			hasContent = hasContent || strings.TrimSpace(text) != ""
			if out.Len()+len(text)+len(name)+100 > uploadTextLimit-1024 {
				return nil, "", "", errors.New("extracted Office content exceeds 2 MiB; split the document")
			}
			fmt.Fprintf(&out, "## %s\n\n%s\n\n", path.Base(name), text)
		}
	default:
		return nil, "", "", errors.New("this ZIP is not a supported DOCX, XLSX, or PPTX document; upload its individual source files")
	}
	if !hasContent {
		return nil, "", "", errors.New("this Office file has no readable text or cell values; export it as PDF for AI reading, or upload its images")
	}
	if out.Len() > uploadTextLimit-1024 {
		return nil, "", "", errors.New("the extracted Office content exceeds 2 MiB; split the document")
	}
	return []byte(out.String()), mediaType, method, nil
}

func sortedOfficeParts(parts map[string]*zip.File, prefix string, include func(string) bool) []string {
	var names []string
	for name := range parts {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".xml") && include(name) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) < len(names[j])
		}
		return names[i] < names[j]
	})
	return names
}

func officeText(data []byte, textTag string) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("could not read Office XML: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == textTag {
				var text string
				if err := decoder.DecodeElement(&text, &t); err != nil {
					return "", err
				}
				out.WriteString(text)
			}
			if t.Name.Local == "tab" {
				out.WriteString("\t")
			}
			if t.Name.Local == "br" {
				out.WriteString("\n")
			}
		case xml.EndElement:
			if t.Name.Local == "p" || t.Name.Local == "tr" {
				out.WriteString("\n")
			}
			if t.Name.Local == "tc" {
				out.WriteString("\t")
			}
		}
	}
	return strings.TrimSpace(out.String()), nil
}
