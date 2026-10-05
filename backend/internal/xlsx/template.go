package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// EmployeeImportTemplate returns a workbook with Chinese headers and a separate
// instructions sheet, keeping employee data on the first sheet.
func EmployeeImportTemplate() ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	files := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="员工导入" sheetId="1" r:id="rId1"/><sheet name="填写说明" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`,
	}
	files["xl/styles.xml"] = `<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="宋体"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="49" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`
	files["xl/worksheets/sheet1.xml"] = templateSheet([][]string{
		{"员工编号", "姓名", "手机号", "部门", "状态"},
	}, []int{24, 20, 24, 24, 24}, 24)
	files["xl/worksheets/sheet2.xml"] = templateSheet([][]string{
		{"字段", "是否必填", "填写说明", "示例"},
		{"员工编号", "必填", "1–32 位英文字母、数字、点、下划线或短横线；不能与已有员工或本表其他员工重复。请按文本填写，保留前导零。", "0001"},
		{"姓名", "必填", "填写员工真实姓名，最多 33 个汉字或 100 个英文字母；中英文混填时请适当缩短。", "张三"},
		{"手机号", "必填", "11 位中国大陆手机号，以 13–19 开头；不能与已有员工或本表其他员工重复。", "13800138000"},
		{"部门", "必填", "填写所属部门，最多 33 个汉字或 100 个英文字母；中英文混填时请适当缩短。", "财务部"},
		{"状态", "可留空", "填写“正常”或“冻结”；留空默认为正常。也支持 ACTIVE（正常）、FROZEN（冻结）。", "正常"},
		{"填写位置", "", "在“员工导入”工作表第 2 行起填写，每行一名员工；保留表头和列顺序。此说明表不会导入。", ""},
		{"导入限制", "", "仅支持 XLSX，文件不超过 5 MB，最多 1000 条数据；不要填写公式。仅新建员工，不覆盖已有档案。", ""},
		{"导入流程", "", "上传后先查看预览；确认后只创建有效行，错误行可下载报告并修正后重新上传。", ""},
	}, []int{24, 16, 80, 24}, 64)
	for name, content := range files {
		entry, err := archive.Create(name)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func templateSheet(rows [][]string, widths []int, rowHeight int) string {
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cols>`)
	for i, width := range widths {
		fmt.Fprintf(&sheet, `<col min="%d" max="%d" width="%d" customWidth="1" style="1"/>`, i+1, i+1, width)
	}
	sheet.WriteString(`</cols><sheetData>`)
	for ri, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d" ht="%d" customHeight="1">`, ri+1, rowHeight)
		for ci, value := range row {
			var escaped bytes.Buffer
			_ = xml.EscapeText(&escaped, []byte(value))
			fmt.Fprintf(&sheet, `<c r="%c%d" s="1" t="inlineStr"><is><t>%s</t></is></c>`, 'A'+ci, ri+1, escaped.String())
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	return sheet.String()
}
