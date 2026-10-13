package acceptance

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
	"github.com/EziosWJ/canteen-wallet/backend/internal/terminal"
)

func (e *env) createTerminal(name string) terminal.Summary {
	e.t.Helper()
	var item terminal.Summary
	e.post(e.public.URL, "/api/admin/terminals", e.adminToken, map[string]string{"name": name}).expect(e.t, http.StatusCreated, &item)
	return item
}

func (e *env) terminalKey(id string) string {
	e.t.Helper()
	var reply struct {
		Key string `json:"key"`
	}
	e.get(e.public.URL, "/api/admin/terminals/"+id+"/credential", e.adminToken).expect(e.t, http.StatusOK, &reply)
	return reply.Key
}

func (e *env) terminalList() []terminal.Summary {
	e.t.Helper()
	var reply struct {
		Items []terminal.Summary `json:"items"`
	}
	e.get(e.public.URL, "/api/admin/terminals", e.adminToken).expect(e.t, http.StatusOK, &reply)
	return reply.Items
}

func TestTerminalManagementCreatesPersistentKeysWithoutExposingThemInLists(t *testing.T) {
	e := newEnv(t)
	first := e.createTerminal(" 食堂一号终端 ")
	second := e.createTerminal("食堂二号终端")
	if first.ID == second.ID || first.Name != "食堂一号终端" || !first.Enabled || !first.CredentialAvailable {
		t.Fatalf("unexpected terminal summaries: %+v %+v", first, second)
	}
	key := e.terminalKey(first.ID)
	if key == e.terminalKey(second.ID) || len(key) != 47 {
		t.Fatal("terminal keys must be independent and complete")
	}
	list := e.get(e.public.URL, "/api/admin/terminals", e.adminToken).expect(t, http.StatusOK, nil)
	if strings.Contains(string(list.body), key) || strings.Contains(string(list.body), "credential_hash") {
		t.Fatal("terminal list exposed credentials")
	}
	e.restartServers()
	if e.terminalKey(first.ID) != key {
		t.Fatal("key changed after restarting")
	}
	response := e.get(e.public.URL, "/api/admin/terminals/"+first.ID+"/credential", e.adminToken)
	if response.header("Cache-Control") != "no-store" {
		t.Fatal("key response is cacheable")
	}
}

func TestTerminalManagementRejectsInvalidNamesAndNonAdministrators(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"", "  ", strings.Repeat("a", 101)} {
		e.post(e.public.URL, "/api/admin/terminals", e.adminToken, map[string]string{"name": name}).expect(t, http.StatusBadRequest, nil)
	}
	if len(e.terminalList()) != 0 {
		t.Fatal("invalid names created terminals")
	}
	item := e.createTerminal("正常终端")
	key := e.terminalKey(item.ID)
	for _, credential := range []string{"", key} {
		e.post(e.public.URL, "/api/admin/terminals", credential, map[string]string{"name": "非法创建"}).expect(t, http.StatusUnauthorized, nil)
		for _, suffix := range []string{"/credential", "/config?server_url=http://example.com"} {
			e.get(e.public.URL, "/api/admin/terminals/"+item.ID+suffix, credential).expect(t, http.StatusUnauthorized, nil)
		}
		e.post(e.public.URL, "/api/admin/terminals/"+item.ID+"/reset-key", credential, nil).expect(t, http.StatusUnauthorized, nil)
		e.request(http.MethodPatch, e.public.URL, "/api/admin/terminals/"+item.ID, credential, map[string]bool{"enabled": false}).expect(t, http.StatusUnauthorized, nil)
	}
	e.request(http.MethodPatch, e.public.URL, "/api/admin/terminals/"+item.ID, e.adminToken, map[string]string{"name": " "}).expect(t, http.StatusBadRequest, nil)
	e.request(http.MethodPatch, e.public.URL, "/api/admin/terminals/missing", e.adminToken, map[string]string{"name": "名称"}).expect(t, http.StatusNotFound, nil)
	e.get(e.public.URL, "/api/admin/terminals/missing/credential", e.adminToken).expect(t, http.StatusNotFound, nil)
}

func TestTerminalResetAndEnableApplyToAllEndpointsAndKeepHistory(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("T007", "13800000701", "终端测试员工")
	e.recharge(worker.id, 5000, "R-T007")
	item := e.createTerminal("旧名称")
	key := e.terminalKey(item.ID)
	issued := e.issue(worker.session, "")
	var consumed scanResult
	e.post(e.public.URL, "/api/v1/terminal/scan", key, map[string]string{"token": issued.Token}).expect(t, http.StatusOK, &consumed)
	if consumed.Status != "SUCCESS" {
		t.Fatalf("scan without heartbeat: %+v", consumed)
	}
	if e.terminalList()[0].LastSeenAt != "" {
		t.Fatal("scan invented a heartbeat")
	}
	e.request(http.MethodPatch, e.public.URL, "/api/admin/terminals/"+item.ID, e.adminToken, map[string]any{"name": "新名称", "enabled": false}).expect(t, http.StatusNoContent, nil)
	assertTerminalDenied(t, e, key)
	var reset struct {
		Key string `json:"key"`
	}
	e.post(e.public.URL, "/api/admin/terminals/"+item.ID+"/reset-key", e.adminToken, nil).expect(t, http.StatusOK, &reset)
	if reset.Key == key || e.terminalKey(item.ID) != reset.Key {
		t.Fatal("reset did not replace the key")
	}
	assertTerminalDenied(t, e, reset.Key) // Reset must preserve the disabled state.
	e.request(http.MethodPatch, e.public.URL, "/api/admin/terminals/"+item.ID, e.adminToken, map[string]bool{"enabled": true}).expect(t, http.StatusNoContent, nil)
	assertTerminalDenied(t, e, key)
	e.post(e.public.URL, "/api/v1/terminal/heartbeat", reset.Key, map[string]string{"scanner_status": "READY", "voice_status": "UNAVAILABLE"}).expect(t, http.StatusOK, nil)
	var replay scanResult
	e.post(e.public.URL, "/api/v1/terminal/scan", reset.Key, map[string]string{"token": issued.Token}).expect(t, http.StatusOK, &replay)
	if replay.TransactionID != consumed.TransactionID || len(e.transactions(worker.id, "CONSUME")) != 1 {
		t.Fatal("management changes altered consumption history")
	}
	listed := e.terminalList()[0]
	if listed.ID != item.ID || listed.Name != "新名称" || !listed.Enabled || listed.LastSeenAt == "" || listed.VoiceStatus != "UNAVAILABLE" {
		t.Fatalf("unexpected updated terminal: %+v", listed)
	}
	for _, event := range e.scanEvents() {
		if event.TerminalID != item.ID {
			t.Fatal("scan history lost terminal identity")
		}
	}
	found := map[string]bool{}
	for _, audit := range e.auditEvents(e.adminToken) {
		if strings.Contains(audit.Details, key) || strings.Contains(audit.Details, reset.Key) {
			t.Fatal("audit exposed key")
		}
		found[audit.Action] = true
	}
	for _, action := range []string{"TERMINAL_CREATED", "TERMINAL_UPDATED", "TERMINAL_KEY_RESET"} {
		if !found[action] {
			t.Fatalf("missing audit %s", action)
		}
	}
}

func assertTerminalDenied(t *testing.T, e *env, key string) {
	t.Helper()
	for _, base := range []string{e.public.URL, e.internal.URL} {
		e.post(base, "/api/v1/terminal/scan", key, map[string]string{"token": "not-a-token"}).expect(t, http.StatusUnauthorized, nil)
		e.post(base, "/api/v1/terminal/heartbeat", key, map[string]string{"scanner_status": "READY", "voice_status": "UNAVAILABLE"}).expect(t, http.StatusUnauthorized, nil)
		for _, path := range []string{"pending", "pending/recent", "pending/missing", "events"} {
			e.get(base, "/api/v1/terminal/"+path, key).expect(t, http.StatusUnauthorized, nil)
		}
	}
}

func TestTerminalConfigDownloadsHTTPAndHTTPSAndRejectsInvalidAddresses(t *testing.T) {
	e := newEnv(t)
	item := e.createTerminal("配置终端")
	key := e.terminalKey(item.ID)
	for _, base := range []string{"http://192.168.1.10:5001", "https://canteen.example.com/prefix/"} {
		response := e.get(e.public.URL, "/api/admin/terminals/"+item.ID+"/config?server_url="+url.QueryEscape(base), e.adminToken).expect(t, http.StatusOK, nil)
		for _, line := range []string{"CANTEEN_SCANNER_DEVICE='/dev/ttyACM0'\n", "CANTEEN_SCANNER_MODE='serial'\n", "CANTEEN_SCANNER_BAUD='9600'\n"} {
			if !strings.Contains(string(response.body), line) {
				t.Fatalf("downloaded config is missing serial scanner setting %q", line)
			}
		}
		if !strings.Contains(string(response.body), "CANTEEN_SERVER_URL='"+strings.TrimRight(base, "/")+"'") || !strings.Contains(string(response.body), key) || response.header("Cache-Control") != "no-store" || !strings.Contains(response.header("Content-Disposition"), "attachment") {
			t.Fatal("config download did not match the actual endpoint/key or was cacheable")
		}
	}
	for _, base := range []string{"", "ftp://host", "http://user:password@host", "http://host?key=secret", "http://host/#fragment", "http://host/\nBAD=value"} {
		e.get(e.public.URL, "/api/admin/terminals/"+item.ID+"/config?server_url="+url.QueryEscape(base), e.adminToken).expect(t, http.StatusBadRequest, nil)
	}
}

func TestTerminalConfigDownloadsSelectedScannerSettings(t *testing.T) {
	e := newEnv(t)
	item := e.createTerminal("串口配置终端")
	for _, tc := range []struct{ mode, device, baud string }{
		{"serial", "/dev/ttyUSB1", "115200"},
		{"serial", "/dev/scanner's-port", "19200"},
		{"evdev", "/dev/input/event7", ""},
		{"hidraw", "/dev/hidraw2", ""},
	} {
		query := url.Values{"server_url": {"http://localhost:5001"}, "scanner_mode": {tc.mode}, "scanner_device": {tc.device}, "scanner_baud": {tc.baud}}
		response := e.get(e.public.URL, "/api/admin/terminals/"+item.ID+"/config?"+query.Encode(), e.adminToken).expect(t, http.StatusOK, nil)
		body := string(response.body)
		quotedDevice := "'" + strings.ReplaceAll(tc.device, "'", "'\"'\"'") + "'"
		if !strings.Contains(body, "CANTEEN_SCANNER_MODE='"+tc.mode+"'\n") || !strings.Contains(body, "CANTEEN_SCANNER_DEVICE="+quotedDevice+"\n") {
			t.Fatal("download ignored selected scanner mode or device")
		}
		if tc.mode == "serial" {
			if !strings.Contains(body, "CANTEEN_SCANNER_BAUD='"+tc.baud+"'\n") {
				t.Fatal("download ignored selected baud rate")
			}
		} else if strings.Contains(body, "CANTEEN_SCANNER_BAUD=") {
			t.Fatal("non-serial config contains baud rate")
		}
	}
	for _, query := range []url.Values{
		{"scanner_mode": {"unknown"}},
		{"scanner_device": {"/dev/ttyACM0\nBAD=value"}},
		{"scanner_device": {"relative-path"}},
		{"scanner_baud": {"unsupported"}},
		{"scanner_baud": {"0"}},
	} {
		query.Set("server_url", "http://localhost:5001")
		e.get(e.public.URL, "/api/admin/terminals/"+item.ID+"/config?"+query.Encode(), e.adminToken).expect(t, http.StatusBadRequest, nil)
	}
}

func TestTerminalUpgradePreservesLegacyCredentialsUntilExplicitReset(t *testing.T) {
	e := newEnv(t)
	key := e.terminal("legacy-terminal", "旧终端")
	// Recreate the prior schema and migration state, preserving existing rows.
	for _, statement := range []string{`ALTER TABLE terminals DROP COLUMN credential`, `DELETE FROM schema_migrations WHERE name='0071_terminal_credential.sql'`} {
		if _, err := e.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(t.Context(), e.db); err != nil {
		t.Fatal(err)
	}
	e.restartServers()
	e.post(e.public.URL, "/api/v1/terminal/heartbeat", key, map[string]string{"scanner_status": "READY", "voice_status": "UNAVAILABLE"}).expect(t, http.StatusOK, nil)
	if item := e.terminalList()[0]; item.CredentialAvailable || item.ID != "legacy-terminal" {
		t.Fatalf("unexpected upgraded terminal: %+v", item)
	}
	e.get(e.public.URL, "/api/admin/terminals/legacy-terminal/credential", e.adminToken).expect(t, http.StatusConflict, nil)
	e.get(e.public.URL, "/api/admin/terminals/legacy-terminal/config?server_url=http://host", e.adminToken).expect(t, http.StatusConflict, nil)
	e.post(e.public.URL, "/api/admin/terminals/legacy-terminal/reset-key", e.adminToken, nil).expect(t, http.StatusOK, nil)
	assertTerminalDenied(t, e, key)
	if e.terminalKey("legacy-terminal") == "" {
		t.Fatal("reset did not make the key recoverable")
	}
}

func TestPublicTerminalAPIObservesEmployeeConfirmationWithoutAuthorizingIt(t *testing.T) {
	e := newEnv(t)
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 30*time.Minute, 30*time.Minute)
	worker := e.newEmployee("T008", "13800000702", "确认测试员工")
	e.recharge(worker.id, 5000, "R-T008")
	item := e.createTerminal("完整流程终端")
	key := e.terminalKey(item.ID)
	first := e.issue(worker.session, "")
	e.post(e.public.URL, "/api/v1/terminal/scan", key, map[string]string{"token": first.Token}).expect(t, http.StatusOK, nil)
	e.ageFirstScanEvents(4 * time.Second)
	next := e.issue(worker.session, newPresentationID(t))
	var pending scanResult
	e.post(e.public.URL, "/api/v1/terminal/scan", key, map[string]string{"token": next.Token}).expect(t, http.StatusOK, &pending)
	if pending.Status != "PENDING" {
		t.Fatalf("expected employee confirmation: %+v", pending)
	}
	e.get(e.public.URL, "/api/v1/terminal/pending/"+pending.PendingID, key).expect(t, http.StatusOK, nil)
	e.get(e.public.URL, "/api/v1/terminal/pending/recent", key).expect(t, http.StatusOK, nil)
	e.get(e.public.URL, "/api/v1/terminal/events", key).expect(t, http.StatusOK, nil)
	e.post(e.public.URL, "/api/v1/terminal/pending/"+pending.PendingID+"/confirm", key, nil).expect(t, http.StatusMethodNotAllowed, nil)
	e.post(e.public.URL, "/api/me/pending-consumptions/"+pending.PendingID+"/confirm", key, nil).expect(t, http.StatusUnauthorized, nil)
	response, confirmed := e.decide(worker.session, pending.PendingID, "confirm")
	response.expect(t, http.StatusOK, nil)
	var observed scanResult
	e.get(e.public.URL, "/api/v1/terminal/pending/"+pending.PendingID, key).expect(t, http.StatusOK, &observed)
	if observed.Status != "SUCCESS" || observed.TransactionID != confirmed.TransactionID || len(e.transactions(worker.id, "CONSUME")) != 2 {
		t.Fatal("public terminal did not observe the employee's final result")
	}
}

func TestLegacyTerminalIDsRemainManageableWhenURLContainsEscapedCharacters(t *testing.T) {
	e := newEnv(t)
	id := "legacy/入口"
	key := e.terminal(id, "旧编号终端")
	base := "/api/admin/terminals/" + url.PathEscape(id)
	var reply struct {
		Key string `json:"key"`
	}
	e.get(e.public.URL, base+"/credential", e.adminToken).expect(t, http.StatusOK, &reply)
	if reply.Key != key {
		t.Fatal("could not retrieve legacy terminal key")
	}
	e.request(http.MethodPatch, e.public.URL, base, e.adminToken, map[string]string{"name": "已改名"}).expect(t, http.StatusNoContent, nil)
	if e.terminalList()[0].Name != "已改名" {
		t.Fatal("escaped legacy terminal was not updated")
	}
}
