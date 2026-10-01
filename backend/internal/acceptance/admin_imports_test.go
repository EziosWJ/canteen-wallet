package acceptance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/EziosWJ/canteen-wallet/backend/internal/xlsx"
)

type importPreviewResult struct {
	PreviewID string `json:"preview_id"`
	Rows      []struct {
		Row int `json:"row"`
	} `json:"rows"`
	Errors []struct {
		Row     int    `json:"row"`
		Message string `json:"message"`
	} `json:"errors"`
	TotalRows int `json:"total_rows"`
	ValidRows int `json:"valid_rows"`
	ErrorRows int `json:"error_rows"`
}

func TestAdminEmployeeImportTemplateAndMixedRows(t *testing.T) {
	e := newEnv(t)
	template := e.get(e.public.URL, "/api/admin/imports/template", e.adminToken).expect(t, http.StatusOK, nil)
	if !strings.Contains(template.header("Content-Type"), "spreadsheetml.sheet") || !strings.Contains(template.header("Content-Disposition"), "employee-import-template.xlsx") {
		t.Fatalf("template headers are not an XLSX download: %v", template.headers)
	}
	matrix, err := xlsx.ReadFirstSheet(template.body)
	if err != nil || len(matrix) != 1 || strings.Join(matrix[0][:5], ",") != "employee_no,name,phone,department,status" {
		t.Fatalf("template columns do not match import order: matrix=%v err=%v", matrix, err)
	}

	response := uploadEmployeeWorkbook(t, e, [][]string{
		{"IMP-001", "小明", "13900000701", "研发", "ACTIVE"},
		{"bad number", "", "123", "", "INVALID"},
	}, http.StatusOK)
	var preview importPreviewResult
	if err := json.Unmarshal(response.body, &preview); err != nil {
		t.Fatalf("decode import preview: %v; body=%s", err, response.body)
	}
	if preview.PreviewID == "" || preview.TotalRows != 2 || preview.ValidRows != 1 || preview.ErrorRows != 1 || len(preview.Errors) != 1 {
		t.Fatalf("preview statistics are incorrect: %+v", preview)
	}
	confirmed := e.post(e.public.URL, "/api/admin/imports/"+preview.PreviewID+"/confirm", e.adminToken, map[string]any{}).expect(t, http.StatusOK, nil)
	var outcome struct {
		Created []struct {
			Employee struct {
				EmployeeNo string `json:"employee_no"`
			} `json:"employee"`
			TemporaryPassword string `json:"temporary_password"`
		} `json:"created"`
		Errors []struct {
			Row     int    `json:"row"`
			Message string `json:"message"`
		} `json:"errors"`
		CreatedRows int `json:"created_rows"`
	}
	if err := json.Unmarshal(confirmed.body, &outcome); err != nil {
		t.Fatalf("decode import confirm: %v", err)
	}
	if len(outcome.Created) != 1 || outcome.Created[0].Employee.EmployeeNo != "IMP-001" || outcome.Created[0].TemporaryPassword == "" || outcome.CreatedRows != 1 || len(outcome.Errors) != 1 {
		t.Fatalf("mixed import did not create only valid row: %+v", outcome)
	}
	replay := e.post(e.public.URL, "/api/admin/imports/"+preview.PreviewID+"/confirm", e.adminToken, map[string]any{})
	if replay.status != http.StatusConflict || strings.Contains(string(replay.body), outcome.Created[0].TemporaryPassword) || strings.Contains(string(replay.body), "temporary_password") {
		t.Fatalf("confirm replay leaked or reissued temporary credentials: status=%d body=%s", replay.status, replay.body)
	}
	errorCSV := e.get(e.public.URL, "/api/admin/imports/"+preview.PreviewID+"/errors", e.adminToken).expect(t, http.StatusOK, nil)
	if !strings.HasPrefix(errorCSV.header("Content-Type"), "text/csv") || !strings.Contains(string(errorCSV.body), "row,message") || !strings.Contains(string(errorCSV.body), "invalid employee fields") {
		t.Fatalf("error report is not downloadable with row details: headers=%v body=%s", errorCSV.headers, errorCSV.body)
	}
}

func TestAdminEmployeeImportReportsPreviewConflictExpiryAndFileLimit(t *testing.T) {
	e := newEnv(t)

	conflict := uploadEmployeeWorkbook(t, e, [][]string{{"RACE-01", "预览冲突", "13900000702", "研发", "ACTIVE"}}, http.StatusOK)
	var preview importPreviewResult
	if err := json.Unmarshal(conflict.body, &preview); err != nil {
		t.Fatal(err)
	}
	e.newEmployee("RACE-01", "13900000702", "已存在")
	confirmed := e.post(e.public.URL, "/api/admin/imports/"+preview.PreviewID+"/confirm", e.adminToken, map[string]any{}).expect(t, http.StatusOK, nil)
	var outcome struct {
		Created []any `json:"created"`
		Errors  []struct {
			Row     int    `json:"row"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(confirmed.body, &outcome); err != nil {
		t.Fatal(err)
	}
	if len(outcome.Created) != 0 || len(outcome.Errors) != 1 || outcome.Errors[0].Row != 2 || outcome.Errors[0].Message != "employee number or phone already exists" {
		t.Fatalf("preview-time conflict is not reported accurately: %+v", outcome)
	}

	expired := uploadEmployeeWorkbook(t, e, [][]string{{"OLD-01", "过期", "13900000703", "研发", "ACTIVE"}}, http.StatusOK)
	if err := json.Unmarshal(expired.body, &preview); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(t.Context(), `UPDATE import_previews SET created_at='2000-01-01T00:00:00Z' WHERE id=?`, preview.PreviewID); err != nil {
		t.Fatal(err)
	}
	response := e.post(e.public.URL, "/api/admin/imports/"+preview.PreviewID+"/confirm", e.adminToken, map[string]any{})
	if response.status != http.StatusGone || !strings.Contains(string(response.body), "preview expired") {
		t.Fatalf("expired preview was not reported distinctly: status=%d body=%s", response.status, response.body)
	}
	tooLarge := uploadEmployeeWorkbookBytes(t, e, bytes.Repeat([]byte("x"), (6<<20)+1024))
	if tooLarge.status != http.StatusBadRequest {
		t.Fatalf("oversized workbook status=%d, want 400: %s", tooLarge.status, tooLarge.body)
	}
}

func uploadEmployeeWorkbook(t *testing.T, e *env, rows [][]string, wantStatus int) apiResponse {
	t.Helper()
	return uploadEmployeeWorkbookBytes(t, e, employeeWorkbook(t, rows)).expect(t, wantStatus, nil)
}

func uploadEmployeeWorkbookBytes(t *testing.T, e *env, data []byte) apiResponse {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "employees.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, e.public.URL+"/api/admin/imports/preview", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+e.adminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return apiResponse{status: response.StatusCode, body: result, headers: response.Header.Clone()}
}

func employeeWorkbook(t *testing.T, rows [][]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entries := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Employees" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
	}
	allRows := append([][]string{{"employee_no", "name", "phone", "department", "status"}}, rows...)
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	cols := []string{"A", "B", "C", "D", "E"}
	for ri, row := range allRows {
		fmt.Fprintf(&sheet, `<row r="%d">`, ri+1)
		for ci, value := range row {
			if ci >= len(cols) {
				break
			}
			var escaped bytes.Buffer
			if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sheet, `<c r="%s%d" t="inlineStr"><is><t>%s</t></is></c>`, cols[ci], ri+1, escaped.String())
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	entries["xl/worksheets/sheet1.xml"] = sheet.String()
	for name, content := range entries {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
