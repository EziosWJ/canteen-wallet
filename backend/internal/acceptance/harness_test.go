// Package acceptance drives SPEC-002 (#21) through the real public employee
// API and the real internal terminal API over HTTP, against a migrated
// temporary SQLite database. Tests assert only externally observable
// behaviour: HTTP status codes and payloads, account balances, fund-transaction
// counts and amounts, scan events and persisted final states.
//
// Time-dependent behaviour (the 30-second rotation interval, the fixed
// three-second dedupe window and the confirmation deadline) is arranged by
// rewriting the stored time columns as fixtures rather than by sleeping, so the
// suite does not depend on long waits. Only setup touches the database; every
// assertion goes through HTTP.
package acceptance

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/backups"
	"github.com/EziosWJ/canteen-wallet/backend/internal/employees"
	"github.com/EziosWJ/canteen-wallet/backend/internal/httpapi"
	"github.com/EziosWJ/canteen-wallet/backend/internal/meals"
	"github.com/EziosWJ/canteen-wallet/backend/internal/paymenttokens"
	"github.com/EziosWJ/canteen-wallet/backend/internal/recharges"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
	"github.com/EziosWJ/canteen-wallet/backend/internal/terminal"
)

const (
	adminUsername    = "acceptance-admin"
	adminPassword    = "acceptance-admin-password"
	employeePassword = "acceptance-employee-password"
	defaultMealCode  = "LUNCH"
	defaultMealName  = "午餐"
	mealPrice        = int64(1500)
)

type env struct {
	t              *testing.T
	db             *sql.DB
	location       *time.Location
	public         *httptest.Server
	internal       *httptest.Server
	internalHandle http.Handler
	adminToken     string
}

type employee struct {
	id      int64
	name    string
	phone   string
	session string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load time zone: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "acceptance.db")
	db, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	adminService := adminauth.New(db, adminauth.NewTOTP(db))
	employeeService := employees.New(db)
	mealService := meals.New(db, location)
	terminalService := terminal.New(db, mealService, location)
	tokenService := paymenttokens.New(db)
	rechargeService := recharges.New(db)
	backupService := &backups.Service{DB: db, DatabasePath: dbPath}
	e := &env{
		t:              t,
		db:             db,
		location:       location,
		public:         httptest.NewServer(httpapi.Public(db, adminService, employeeService, mealService, tokenService, terminalService, rechargeService, backupService)),
		internalHandle: httpapi.Internal(db, terminalService),
	}
	e.internal = httptest.NewServer(e.internalHandle)
	t.Cleanup(func() {
		e.public.Close()
		e.internal.Close()
		db.Close()
	})
	if _, err := adminService.CreateAdmin(t.Context(), adminUsername, adminPassword); err != nil {
		t.Fatalf("create administrator: %v", err)
	}
	uri, err := adminauth.EnrollTOTP(t.Context(), db, adminUsername)
	if err != nil {
		t.Fatalf("enroll administrator second factor: %v", err)
	}
	secret, err := totpSecret(uri)
	if err != nil {
		t.Fatalf("read second factor secret: %v", err)
	}
	var session struct {
		AccessToken string `json:"access_token"`
	}
	e.post(e.public.URL, "/api/admin/login", "", map[string]string{
		"username": adminUsername, "password": adminPassword,
		"second_factor_code": totpCode(secret, time.Now()),
	}).expect(t, http.StatusOK, &session)
	if session.AccessToken == "" {
		t.Fatal("administrator session is empty")
	}
	e.adminToken = session.AccessToken
	return e
}

func totpSecret(uri string) ([]byte, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(parsed.Query().Get("secret"))
}

func totpCode(secret []byte, at time.Time) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, secret)
	mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

// --- transport -------------------------------------------------------------

// apiResponse carries one HTTP exchange so call sites can assert on it in a
// single expression.
type apiResponse struct {
	status int
	body   []byte
}

func (r apiResponse) expect(t *testing.T, want int, value any) apiResponse {
	t.Helper()
	if r.status != want {
		t.Fatalf("unexpected status %d, want %d: %s", r.status, want, r.body)
	}
	if value != nil {
		if err := json.Unmarshal(r.body, value); err != nil {
			t.Fatalf("decode response %s: %v", r.body, err)
		}
	}
	return r
}

func (e *env) request(method, base, path, token string, payload any) apiResponse {
	e.t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			e.t.Fatalf("encode request body: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, base+path, body)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		e.t.Fatalf("read response body: %v", err)
	}
	return apiResponse{status: response.StatusCode, body: data}
}

func (e *env) get(base, path, token string) apiResponse {
	return e.request(http.MethodGet, base, path, token, nil)
}

func (e *env) post(base, path, token string, payload any) apiResponse {
	return e.request(http.MethodPost, base, path, token, payload)
}

func (e *env) put(base, path, token string, payload any) apiResponse {
	return e.request(http.MethodPut, base, path, token, payload)
}

// --- response shapes -------------------------------------------------------

type issuedToken struct {
	Token          string    `json:"token"`
	PresentationID string    `json:"presentation_id"`
	ServerTime     time.Time `json:"server_time"`
	RefreshAfter   time.Time `json:"refresh_after"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type scanResult struct {
	Status           string     `json:"status"`
	Code             string     `json:"code"`
	Message          string     `json:"message"`
	EmployeeName     string     `json:"employee_name"`
	MealCode         string     `json:"meal_code"`
	MealName         string     `json:"meal_name"`
	AmountCents      int64      `json:"amount_cents"`
	TransactionID    int64      `json:"transaction_id"`
	TransactionNo    string     `json:"transaction_no"`
	ConsumptionNo    string     `json:"consumption_no"`
	OccurredAt       string     `json:"occurred_at"`
	PendingID        string     `json:"pending_id"`
	ExpiresAt        *time.Time `json:"expires_at"`
	Replayed         bool       `json:"replayed"`
	DuplicateTrigger bool       `json:"duplicate_trigger"`
}

type presentationStatus struct {
	PresentationID string     `json:"presentation_id"`
	State          string     `json:"state"`
	ServerTime     time.Time  `json:"server_time"`
	Result         scanResult `json:"result"`
}

type fundEntry struct {
	ID                 int64  `json:"id"`
	Type               string `json:"type"`
	Amount             int64  `json:"amount_cents"`
	BalanceBefore      int64  `json:"before_balance_cents"`
	BalanceAfter       int64  `json:"after_balance_cents"`
	BusinessType       string `json:"business_type"`
	BusinessID         string `json:"business_id"`
	RelatedTransaction int64  `json:"related_transaction_id"`
	AdministratorID    int64  `json:"administrator_id"`
	TerminalID         string `json:"terminal_id"`
	Reason             string `json:"reason"`
	CreatedAt          string `json:"created_at"`
}

type scanEvent struct {
	ID          int64  `json:"id"`
	TerminalID  string `json:"terminal_id"`
	EmployeeID  int64  `json:"employee_id"`
	ResultCode  string `json:"result_code"`
	Transaction int64  `json:"transaction_id"`
}

// --- fixtures --------------------------------------------------------------

func (e *env) newEmployee(employeeNo, phone, name string) *employee {
	e.t.Helper()
	var created struct {
		Employee struct {
			ID int64 `json:"id"`
		} `json:"employee"`
		TemporaryPassword string `json:"temporary_password"`
	}
	e.post(e.public.URL, "/api/admin/employees", e.adminToken, map[string]string{
		"employee_no": employeeNo, "name": name, "phone": phone, "department": "研发",
	}).expect(e.t, http.StatusCreated, &created)
	item := &employee{id: created.Employee.ID, name: name, phone: phone}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	e.post(e.public.URL, "/api/auth/login", "", map[string]string{
		"phone": phone, "password": created.TemporaryPassword,
	}).expect(e.t, http.StatusOK, &login)
	e.post(e.public.URL, "/api/me/change-password", login.AccessToken, map[string]string{
		"old_password": created.TemporaryPassword, "new_password": employeePassword,
	}).expect(e.t, http.StatusOK, &login)
	item.session = login.AccessToken
	return item
}

// login starts a new session for the same employee. Because only one session
// may be active, this revokes the previous one.
func (e *env) login(item *employee) string {
	e.t.Helper()
	var session struct {
		AccessToken string `json:"access_token"`
	}
	e.post(e.public.URL, "/api/auth/login", "", map[string]string{
		"phone": item.phone, "password": employeePassword,
	}).expect(e.t, http.StatusOK, &session)
	item.session = session.AccessToken
	return session.AccessToken
}

func (e *env) balance(id int64) int64 {
	e.t.Helper()
	var item struct {
		Balance int64 `json:"balance"`
	}
	e.get(e.public.URL, fmt.Sprintf("/api/admin/employees/%d", id), e.adminToken).expect(e.t, http.StatusOK, &item)
	return item.Balance
}

func (e *env) recharge(id, amount int64, receiptRef string) {
	e.t.Helper()
	e.post(e.public.URL, "/api/admin/recharges", e.adminToken, map[string]any{
		"employee_id": id, "amount_cents": amount, "receipt_ref": receiptRef,
		"collected_at": time.Now().UTC().Format(time.RFC3339Nano), "payment_method": "CASH",
		"idempotency_key": "acceptance-recharge-" + receiptRef,
	}).expect(e.t, http.StatusCreated, nil)
}

func (e *env) transactions(id int64, kind string) []fundEntry {
	e.t.Helper()
	var page struct {
		Items []fundEntry `json:"items"`
	}
	e.get(e.public.URL,
		fmt.Sprintf("/api/admin/transactions?employee_id=%d&type=%s", id, kind), e.adminToken).
		expect(e.t, http.StatusOK, &page)
	return page.Items
}

func (e *env) scanEvents() []scanEvent {
	e.t.Helper()
	var page struct {
		Items []scanEvent `json:"items"`
	}
	e.get(e.public.URL, "/api/admin/scan-events", e.adminToken).expect(e.t, http.StatusOK, &page)
	return page.Items
}

type minuteSpan struct{ from, to int }

// activateMeal makes one meal period cover the current moment, with the meal
// window opening `before` and closing `after` now. The other two periods are
// moved into free one-minute windows first, so no configured window overlaps.
func (e *env) activateMeal(code, name string, price int64, before, after time.Duration) {
	e.t.Helper()
	now := time.Now().In(e.location)
	start := now.Add(-before)
	end := now.Add(after)
	window := minuteSpan{from: start.Hour()*60 + start.Minute(), to: end.Hour()*60 + end.Minute()}
	if window.to <= window.from || window.to > 1440 {
		e.t.Fatalf("meal window %s..%s is invalid", start.Format("15:04"), end.Format("15:04"))
	}
	for _, other := range otherMealCodes(code) {
		e.parkInFreeSlot(other, []minuteSpan{window})
	}
	e.setMeal(code, name, window.from, window.to, price, true)
}

// otherMealCodes lists the configured meal codes apart from the given one.
func otherMealCodes(code string) []string {
	others := make([]string, 0, 2)
	for _, candidate := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
		if candidate != code {
			others = append(others, candidate)
		}
	}
	return others
}

// parkInFreeSlot moves one period into a free one-minute window. The window is
// chosen against the periods that are configured right now, plus any reserved
// span the caller is about to use, so sequential moves never collide.
func (e *env) parkInFreeSlot(code string, reserved []minuteSpan) {
	e.t.Helper()
	slot, ok := firstFreeSlot(append(e.currentSpansExcept(code), reserved...))
	if !ok {
		e.t.Fatalf("no free meal slot left to park %s", code)
	}
	e.setMeal(code, mealName(code), slot.from, slot.to, 0, false)
}

func (e *env) currentSpansExcept(code string) []minuteSpan {
	e.t.Helper()
	rows, err := e.db.QueryContext(e.t.Context(),
		`SELECT start_minute,end_minute FROM meal_periods WHERE code != ?`, code)
	if err != nil {
		e.t.Fatalf("read meal periods: %v", err)
	}
	defer rows.Close()
	spans := make([]minuteSpan, 0, 2)
	for rows.Next() {
		var span minuteSpan
		if err := rows.Scan(&span.from, &span.to); err != nil {
			e.t.Fatalf("scan meal period: %v", err)
		}
		spans = append(spans, span)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("read meal periods: %v", err)
	}
	return spans
}

// firstFreeSlot returns the earliest free one-minute window in the day.
func firstFreeSlot(occupied []minuteSpan) (minuteSpan, bool) {
	for minute := 0; minute < 1440; minute++ {
		conflict := 0
		for _, span := range occupied {
			if minute < span.to && minute+1 > span.from {
				conflict = span.to
				break
			}
		}
		if conflict == 0 {
			return minuteSpan{from: minute, to: minute + 1}, true
		}
		minute = conflict - 1
	}
	return minuteSpan{}, false
}

func (e *env) setMeal(code, name string, startMinute, endMinute int, price int64, enabled bool) {
	e.t.Helper()
	e.put(e.public.URL, "/api/admin/meal-periods/"+code, e.adminToken, map[string]any{
		"name":        name,
		"start_time":  minuteString(startMinute),
		"end_time":    minuteString(endMinute),
		"price_cents": price,
		"enabled":     enabled,
	}).expect(e.t, http.StatusOK, nil)
}

// disableAllMeals leaves every configured period disabled, so no meal can be
// resolved for a scan.
func (e *env) disableAllMeals() {
	e.t.Helper()
	for _, code := range []string{"BREAKFAST", "LUNCH", "DINNER"} {
		e.parkInFreeSlot(code, nil)
	}
}

// mealWindowMinutes returns the configured start and end minute of a period.
func (e *env) mealWindowMinutes(code string) minuteSpan {
	e.t.Helper()
	var span minuteSpan
	if err := e.db.QueryRowContext(e.t.Context(),
		`SELECT start_minute,end_minute FROM meal_periods WHERE code=?`, code).
		Scan(&span.from, &span.to); err != nil {
		e.t.Fatalf("read meal window: %v", err)
	}
	return span
}

func minuteString(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// terminalEvents reads the scan events a terminal is told about, which is what
// the device uses to reconcile its own display.
func (e *env) terminalEvents(credential string, afterID int64, value any) apiResponse {
	e.t.Helper()
	return e.get(e.internal.URL,
		fmt.Sprintf("/api/v1/terminal/events?after_id=%d", afterID), credential).expect(e.t, http.StatusOK, value)
}

// endMealNow shortens the active meal window so it ends in the current minute.
// Shrinking a window cannot overlap another period, so this is always accepted.
func (e *env) endMealNow(code string) {
	e.t.Helper()
	span := e.mealWindowMinutes(code)
	localNow := time.Now().In(e.location)
	minute := localNow.Hour()*60 + localNow.Minute()
	if minute <= span.from {
		e.t.Fatalf("meal window %s already ended", code)
	}
	e.setMeal(code, mealName(code), span.from, minute, e.mealPriceCents(code), true)
}

// mealPriceCents reads the configured price of a period.
func (e *env) mealPriceCents(code string) int64 {
	e.t.Helper()
	var price int64
	if err := e.db.QueryRowContext(e.t.Context(),
		`SELECT price_cents FROM meal_periods WHERE code=?`, code).Scan(&price); err != nil {
		e.t.Fatalf("read meal price: %v", err)
	}
	return price
}

// reconfigureMeal changes the meal name and price after a scan, standing in for
// an administrator editing the meal configuration mid-flow.
func (e *env) reconfigureMeal(code, name string, price int64) {
	e.t.Helper()
	span := e.mealWindowMinutes(code)
	e.setMeal(code, name, span.from, span.to, price, true)
}

func mealName(code string) string {
	switch code {
	case "BREAKFAST":
		return "早餐"
	case "DINNER":
		return "晚餐"
	default:
		return "午餐"
	}
}

func (e *env) terminal(id, name string) string {
	e.t.Helper()
	credential, err := terminal.Provision(e.t.Context(), e.db, id, name)
	if err != nil {
		e.t.Fatalf("provision terminal %s: %v", id, err)
	}
	return credential
}

func (e *env) issue(session, presentationID string) issuedToken {
	e.t.Helper()
	response := e.issueRaw(session, presentationID)
	response.expect(e.t, http.StatusOK, nil)
	var issued issuedToken
	if err := json.Unmarshal(response.body, &issued); err != nil {
		e.t.Fatalf("decode issued token %s: %v", response.body, err)
	}
	if issued.Token == "" || issued.PresentationID == "" {
		e.t.Fatalf("payment token response is incomplete: %s", response.body)
	}
	return issued
}

func (e *env) issueRaw(session, presentationID string) apiResponse {
	e.t.Helper()
	var payload any
	if presentationID != "" {
		payload = map[string]string{"presentation_id": presentationID}
	}
	return e.post(e.public.URL, "/api/me/payment-token", session, payload)
}

// scan posts a code to the terminal API and requires a well-formed success
// response, which every business outcome of a valid code uses.
func (e *env) scan(credential, token string) scanResult {
	e.t.Helper()
	response, result := e.scanOnce(credential, token)
	if response.status != http.StatusOK {
		e.t.Fatalf("scan returned %d: %s", response.status, response.body)
	}
	return result
}

// postOnce is a goroutine-safe variant of post: it reports transport failures
// through its error result instead of failing the test, because t.Fatalf must
// only be called from the goroutine running the test.
func (e *env) postOnce(base, path, token string, payload any) (apiResponse, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return apiResponse{}, err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(http.MethodPost, base+path, body)
	if err != nil {
		return apiResponse{}, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return apiResponse{}, err
	}
	return apiResponse{status: response.StatusCode, body: data}, nil
}

// decideOnce is a goroutine-safe variant of decide used by the concurrency
// tests, where several decisions race for one final state.
func (e *env) decideOnce(session, pendingID, decision string) (apiResponse, scanResult) {
	response, err := e.postOnce(e.public.URL,
		fmt.Sprintf("/api/me/pending-consumptions/%s/%s", pendingID, decision), session, nil)
	if err != nil {
		return apiResponse{}, scanResult{}
	}
	var result scanResult
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &result); err != nil {
			return response, scanResult{}
		}
	}
	return response, result
}

// scanOnce is safe to call from any goroutine: it reports problems through its
// return values instead of failing the test directly.
func (e *env) scanOnce(credential, token string) (apiResponse, scanResult) {
	var result scanResult
	response := e.post(e.internal.URL, "/api/v1/terminal/scan", credential, map[string]string{"token": token})
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &result); err != nil {
			return response, scanResult{}
		}
	}
	return response, result
}

func (e *env) presentation(session string) presentationStatus {
	e.t.Helper()
	return e.presentationByID(session, "")
}

func (e *env) presentationByID(session, presentationID string) presentationStatus {
	e.t.Helper()
	path := "/api/me/payment-presentation"
	if presentationID != "" {
		path += "?id=" + url.QueryEscape(presentationID)
	}
	var status presentationStatus
	e.get(e.public.URL, path, session).expect(e.t, http.StatusOK, &status)
	return status
}

func (e *env) decide(session, pendingID, decision string) (apiResponse, scanResult) {
	e.t.Helper()
	response := e.post(e.public.URL,
		fmt.Sprintf("/api/me/pending-consumptions/%s/%s", pendingID, decision), session, nil)
	var result scanResult
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &result); err != nil {
			e.t.Fatalf("decode decision result %s: %v", response.body, err)
		}
	}
	return response, result
}

func (e *env) terminalPending(credential, pendingID string) (apiResponse, scanResult) {
	e.t.Helper()
	response := e.get(e.internal.URL, "/api/v1/terminal/pending/"+pendingID, credential)
	var result scanResult
	if response.status == http.StatusOK {
		if err := json.Unmarshal(response.body, &result); err != nil {
			e.t.Fatalf("decode pending status %s: %v", response.body, err)
		}
	}
	return response, result
}

// --- time and state fixtures ----------------------------------------------
//
// The service reads wall-clock time directly, so tests arrange elapsed windows
// by rewriting stored time columns. Every fixture below only changes setup
// facts; the assertions still run through HTTP.

// injectToken stores an additional active payment code for a presentation,
// returning its plaintext value. Rotation normally needs the 30-second refresh
// interval to elapse, which the fixture replaces with an explicit issue time.
// The token is written exactly as the service writes it (hash only, never the
// plaintext), so the terminal API validates it through the normal path.
func (e *env) injectToken(session, presentationID string, issuedAt time.Time, validFor time.Duration) string {
	e.t.Helper()
	var employeeID, sessionID int64
	err := e.db.QueryRowContext(e.t.Context(),
		`SELECT employee_id,session_id FROM payment_presentations WHERE id=?`, presentationID).
		Scan(&employeeID, &sessionID)
	if err != nil {
		e.t.Fatalf("locate session for presentation: %v", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		e.t.Fatalf("generate token: %v", err)
	}
	value := "pmt_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(value))
	if _, err := e.db.ExecContext(e.t.Context(), `INSERT INTO payment_tokens
		(token_hash,employee_id,session_id,issued_at,expires_at,presentation_id)
		VALUES (?,?,?,?,?,?)`, hash[:], employeeID, sessionID,
		issuedAt.Unix(), issuedAt.Add(validFor).Unix(), presentationID); err != nil {
		e.t.Fatalf("insert payment token: %v", err)
	}
	return value
}

// setTokenState parks an issued code in a non-active state. Only terminal
// transitions away from ACTIVE are accepted by the database guard.
func (e *env) setTokenState(token, state string) {
	e.t.Helper()
	hash := sha256.Sum256([]byte(token))
	result, err := e.db.ExecContext(e.t.Context(),
		`UPDATE payment_tokens SET state=? WHERE token_hash=? AND state='ACTIVE'`, state, hash[:])
	if err != nil {
		e.t.Fatalf("update token state: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		e.t.Fatalf("update token state affected %d rows (err %v)", affected, err)
	}
}

// ageFirstScanEvents moves the fixed three-second window into the past by
// backdating the first (non-duplicate) scan events.
func (e *env) ageFirstScanEvents(by time.Duration) {
	e.t.Helper()
	if _, err := e.db.ExecContext(e.t.Context(), `UPDATE scan_events SET received_at_ms=received_at_ms-?
		WHERE first_event_id IS NULL`, by.Milliseconds()); err != nil {
		e.t.Fatalf("age scan events: %v", err)
	}
}

// expirePending moves a confirmation deadline into the past without touching
// the immutable scan snapshot (meal, amount, meal end, business date).
func (e *env) expirePending(pendingID string) {
	e.t.Helper()
	result, err := e.db.ExecContext(e.t.Context(),
		`UPDATE pending_consumptions SET expires_at=? WHERE id=? AND state='PENDING'`,
		time.Now().UTC().Add(-time.Second).Unix(), pendingID)
	if err != nil {
		e.t.Fatalf("expire pending consumption: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		e.t.Fatalf("expire pending consumption affected %d rows (err %v)", affected, err)
	}
}

// setPendingDeadlines rewrites the two deadlines stored with a request, as they
// would read once that moment has passed. The scanner records the earlier of
// "scan + 60s" and the meal end, so both are moved together, matching a request
// raised near the end of its meal. The snapshot trigger forbids updating them in
// place, so the row is replaced with the same identity and content apart from
// the deadlines.
func (e *env) setPendingDeadlines(pendingID string, mealEndAt, expiresAt time.Time) {
	e.t.Helper()
	var id, terminalID, mealCode, mealName, date, state, createdAt string
	var tokenID, employeeID, amount int64
	if err := e.db.QueryRowContext(e.t.Context(), `SELECT id,token_id,terminal_id,employee_id,
		meal_code,meal_name,business_date,amount_cents,state,created_at
		FROM pending_consumptions WHERE id=?`, pendingID).
		Scan(&id, &tokenID, &terminalID, &employeeID, &mealCode, &mealName, &date, &amount,
			&state, &createdAt); err != nil {
		e.t.Fatalf("read pending consumption: %v", err)
	}
	tx, err := e.db.BeginTx(e.t.Context(), nil)
	if err != nil {
		e.t.Fatalf("begin pending rewrite: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(e.t.Context(), `DELETE FROM pending_consumptions WHERE id=?`, pendingID); err != nil {
		e.t.Fatalf("clear pending consumption: %v", err)
	}
	if _, err := tx.ExecContext(e.t.Context(), `INSERT INTO pending_consumptions
		(id,token_id,terminal_id,employee_id,meal_code,meal_name,business_date,amount_cents,
		 meal_end_at,expires_at,state,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, id, tokenID, terminalID, employeeID, mealCode,
		mealName, date, amount, mealEndAt.Unix(), expiresAt.Unix(), state, createdAt); err != nil {
		e.t.Fatalf("restore pending consumption: %v", err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatalf("commit pending rewrite: %v", err)
	}
}

// pendingDeadline reads the stored confirmation deadline of a request.
func (e *env) pendingDeadline(pendingID string) int64 {
	e.t.Helper()
	var expires int64
	if err := e.db.QueryRowContext(e.t.Context(),
		`SELECT expires_at FROM pending_consumptions WHERE id=?`, pendingID).Scan(&expires); err != nil {
		e.t.Fatalf("read pending deadline: %v", err)
	}
	return expires
}

func (e *env) pendingState(pendingID string) (string, string) {
	e.t.Helper()
	var state string
	var code sql.NullString
	if err := e.db.QueryRowContext(e.t.Context(),
		`SELECT state,result_code FROM pending_consumptions WHERE id=?`, pendingID).Scan(&state, &code); err != nil {
		e.t.Fatalf("read pending state: %v", err)
	}
	return state, code.String
}

func (e *env) refund(transactionID int64) {
	e.t.Helper()
	e.post(e.public.URL,
		fmt.Sprintf("/api/admin/transactions/%d/refund", transactionID), e.adminToken, map[string]string{
			"reason":          "验收测试退款",
			"idempotency_key": fmt.Sprintf("acceptance-refund-%d", transactionID),
		}).expect(e.t, http.StatusCreated, nil)
}

// newPresentationID mimics the identifier the employee page generates when the
// employee starts a fresh presentation: prs_ plus 32 hex characters.
func newPresentationID(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate presentation id: %v", err)
	}
	return "prs_" + fmt.Sprintf("%x", raw)
}

// accountID reads the stored account identifier of an employee.
func (e *env) accountID(employeeID int64) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.QueryRowContext(e.t.Context(),
		`SELECT id FROM accounts WHERE employee_id=?`, employeeID).Scan(&id); err != nil {
		e.t.Fatalf("read account id: %v", err)
	}
	return id
}

// adjustBalance moves an employee's balance by the given signed amount without
// touching the meal history, so a confirmation can be checked against an
// exhausted or topped-up account while the login session stays valid.
func (e *env) adjustBalance(employeeID, amountCents int64, reference string) {
	e.t.Helper()
	e.post(e.public.URL, fmt.Sprintf("/api/admin/accounts/%d/adjust", e.accountID(employeeID)),
		e.adminToken, map[string]any{
			"amount_cents":    amountCents,
			"reason":          "验收测试余额调整 " + reference,
			"idempotency_key": "acceptance-adjust-" + reference,
		}).expect(e.t, http.StatusCreated, nil)
}

// employeeBalance reads the balance through the employee's own account view, so
// assertions follow the surface the employee page uses.
func (e *env) employeeBalance(session string) int64 {
	e.t.Helper()
	var item struct {
		Balance int64  `json:"balance"`
		Status  string `json:"status"`
	}
	e.get(e.public.URL, "/api/me/account", session).expect(e.t, http.StatusOK, &item)
	return item.Balance
}

// scanWithoutServer posts a code to a terminal listener that has gone away and
// reports the transport failure instead of failing the test, so a test can
// assert what the terminal does when the server is unreachable.
func (e *env) scanWithoutServer(credential, token string) error {
	raw, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost,
		e.internalURL()+"/api/v1/terminal/scan", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return fmt.Errorf("terminal scan unexpectedly answered with status %d", response.StatusCode)
}

// stopInternal simulates the server becoming unreachable for the terminal.
func (e *env) stopInternal() { e.internal.Close() }

// internalURL returns the address the terminal posts to, so a test can keep
// talking to a stopped listener after it is closed.
func (e *env) internalURL() string { return e.internal.URL }

// startInternal brings the terminal listener back on a new address, which
// stands in for the terminal recovering connectivity.
func (e *env) startInternal() {
	e.t.Helper()
	e.internal = httptest.NewServer(e.internalHandle)
	e.t.Cleanup(e.internal.Close)
}

// setAccountStatus changes the stored value the terminal and employee flows
// must re-check when a pending consumption is confirmed.
func (e *env) setAccountStatus(id int64, status string) {
	e.t.Helper()
	if _, err := e.db.ExecContext(e.t.Context(),
		`UPDATE accounts SET status=? WHERE employee_id=?`, status, id); err != nil {
		e.t.Fatalf("update account status: %v", err)
	}
}

// revokeSession invalidates an employee login session without going through the
// logout endpoint, so a pending confirmation can be re-checked against it.
func (e *env) revokeSession(session string) {
	e.t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(session)
	if err != nil || len(raw) != 32 {
		e.t.Fatalf("session token %q is not a base64 session value", session)
	}
	hash := sha256.Sum256(raw)
	result, err := e.db.ExecContext(e.t.Context(),
		`UPDATE employee_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`,
		time.Now().UTC().Unix(), hash[:])
	if err != nil {
		e.t.Fatalf("revoke session: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		e.t.Fatalf("revoke session affected %d rows (err %v)", affected, err)
	}
}

// postManualSupply registers a fault-supply receipt and posts it as a CONSUME
// transaction, so a self-service count can be checked against the manual-supply
// entrance described in SPEC-003.
func (e *env) postManualSupply(employeeID int64, receiptRef, date string, amount int64) {
	e.t.Helper()
	var created struct {
		ID int64 `json:"id"`
	}
	e.post(e.public.URL, "/api/admin/manual-supplies", e.adminToken, map[string]any{
		"receipt_ref": receiptRef, "employee_id": employeeID, "meal_code": defaultMealCode,
		"business_date": date, "amount_cents": amount, "note": "验收测试补录",
	}).expect(e.t, http.StatusCreated, &created)
	e.post(e.public.URL, fmt.Sprintf("/api/admin/manual-supplies/%d/post", created.ID), e.adminToken,
		map[string]string{"idempotency_key": "acceptance-manual-post-" + receiptRef}).
		expect(e.t, http.StatusCreated, nil)
}

// selfServiceRequiresMeal activates the default meal window for the duration of
// the test, matching the other suites' setup.
func (e *env) selfServiceRequiresMeal() {
	e.t.Helper()
	e.activateMeal(defaultMealCode, defaultMealName, mealPrice, 20*time.Minute, 20*time.Minute)
}
