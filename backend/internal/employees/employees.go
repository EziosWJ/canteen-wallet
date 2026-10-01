package employees

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EziosWJ/canteen-wallet/backend/internal/adminauth"
	"github.com/EziosWJ/canteen-wallet/backend/internal/store"
	"modernc.org/sqlite"
)

var (
	ErrInvalidInput   = errors.New("invalid employee data")
	ErrConflict       = errors.New("employee data conflicts with an existing record")
	ErrNotFound       = errors.New("employee not found")
	ErrInvalidState   = errors.New("invalid employee state transition")
	employeeNoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	phonePattern      = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
)

type Service struct {
	db        *sql.DB
	loginGate chan struct{}
}

type CreateInput struct {
	EmployeeNo string `json:"employee_no"`
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	Department string `json:"department"`
	PhotoURL   string `json:"photo_url"`
	Status     string `json:"status"`
}

type Employee struct {
	ID                 int64   `json:"id"`
	AccountID          int64   `json:"account_id"`
	EmployeeNo         string  `json:"employee_no"`
	Name               string  `json:"name"`
	Phone              string  `json:"phone"`
	Department         string  `json:"department"`
	PhotoURL           *string `json:"photo_url"`
	Status             string  `json:"status"`
	AccountStatus      string  `json:"account_status"`
	Balance            int64   `json:"balance"`
	MustChangePassword bool    `json:"must_change_password"`
}

// SearchInput describes one stable, keyset-paginated employee search. A zero
// limit selects the default page size. Cursor is an opaque token returned by
// the previous page; it is tied to the globally unique employee ID ordering.
type SearchInput struct {
	Query        string
	Department   string
	AccountState string
	Cursor       string
	Limit        int
}

type EmployeePage struct {
	Employees  []Employee `json:"employees"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

func New(db *sql.DB) *Service { return &Service{db: db, loginGate: make(chan struct{}, 2)} }

func (s *Service) Create(ctx context.Context, actorID int64, input CreateInput) (Employee, string, error) {
	input.EmployeeNo = strings.TrimSpace(input.EmployeeNo)
	input.Name = strings.TrimSpace(input.Name)
	input.Phone = normalizePhone(input.Phone)
	input.Department = strings.TrimSpace(input.Department)
	input.PhotoURL = strings.TrimSpace(input.PhotoURL)
	if input.Status == "" {
		input.Status = "ACTIVE"
	}
	if !employeeNoPattern.MatchString(input.EmployeeNo) || len(input.Name) < 1 || len(input.Name) > 100 ||
		!phonePattern.MatchString(input.Phone) || len(input.Department) < 1 || len(input.Department) > 100 ||
		(input.Status != "ACTIVE" && input.Status != "FROZEN") || !validPhotoURL(input.PhotoURL) {
		return Employee{}, "", ErrInvalidInput
	}
	temporaryPassword, err := newTemporaryPassword()
	if err != nil {
		return Employee{}, "", err
	}
	hash, err := adminauth.HashPassword(temporaryPassword)
	if err != nil {
		return Employee{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Employee{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO employees
		(employee_no, name, phone, department, photo_url, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, input.EmployeeNo, input.Name, input.Phone, input.Department,
		nullString(input.PhotoURL), input.Status, hash, now, now)
	if err != nil {
		return Employee{}, "", classifyWrite(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Employee{}, "", err
	}
	accountResult, err := tx.ExecContext(ctx, `INSERT INTO accounts (employee_id, balance, status, created_at, updated_at)
		VALUES (?, 0, ?, ?, ?)`, id, input.Status, now, now)
	if err != nil {
		return Employee{}, "", err
	}
	if err := store.RecordAudit(ctx, tx, actorID, "EMPLOYEE_CREATED", "employee", strconv.FormatInt(id, 10), nil); err != nil {
		return Employee{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Employee{}, "", err
	}
	photo := pointerIfNotEmpty(input.PhotoURL)
	accountID, err := accountResult.LastInsertId()
	if err != nil {
		return Employee{}, "", err
	}
	return Employee{ID: id, AccountID: accountID, EmployeeNo: input.EmployeeNo, Name: input.Name, Phone: input.Phone,
		Department: input.Department, PhotoURL: photo, Status: input.Status, AccountStatus: input.Status,
		Balance: 0, MustChangePassword: true}, temporaryPassword, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Employee, error) {
	const query = `SELECT e.id, a.id, e.employee_no, e.name, e.phone, e.department, e.photo_url,
		e.status, a.status, a.balance, e.must_change_password
		FROM employees e JOIN accounts a ON a.employee_id = e.id WHERE e.id = ?`
	item, err := scanEmployee(s.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Employee{}, ErrNotFound
	}
	return item, err
}

func (s *Service) List(ctx context.Context) ([]Employee, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, a.id, e.employee_no, e.name, e.phone, e.department, e.photo_url,
		e.status, a.status, a.balance, e.must_change_password
		FROM employees e JOIN accounts a ON a.employee_id = e.id ORDER BY e.id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Employee, 0)
	for rows.Next() {
		item, err := scanEmployee(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Search returns employees matching identity fields and exact department or
// account-state filters. Results use employee ID as a stable keyset cursor so
// records can be found past the first page without loading a fixed-size list.
func (s *Service) Search(ctx context.Context, input SearchInput) (EmployeePage, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.Department = strings.TrimSpace(input.Department)
	input.AccountState = strings.TrimSpace(strings.ToUpper(input.AccountState))
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > 100 {
		return EmployeePage{}, ErrInvalidInput
	}
	if input.AccountState != "" && input.AccountState != "ACTIVE" && input.AccountState != "FROZEN" && input.AccountState != "CLOSED" {
		return EmployeePage{}, ErrInvalidInput
	}
	lastID := int64(0)
	if input.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil {
			return EmployeePage{}, ErrInvalidInput
		}
		parsed, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || parsed < 1 {
			return EmployeePage{}, ErrInvalidInput
		}
		lastID = parsed
	}
	search := escapeLike(input.Query)
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, a.id, e.employee_no, e.name, e.phone, e.department, e.photo_url,
		e.status, a.status, a.balance, e.must_change_password
		FROM employees e JOIN accounts a ON a.employee_id = e.id
		WHERE e.id > ?
		AND (? = '' OR e.name LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE
			OR e.employee_no LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE
			OR e.phone LIKE '%' || ? || '%' ESCAPE '\' COLLATE NOCASE)
		AND (? = '' OR e.department = ? COLLATE NOCASE)
		AND (? = '' OR a.status = ?)
		ORDER BY e.id ASC LIMIT ?`, lastID, search, search, search, search,
		input.Department, input.Department, input.AccountState, input.AccountState, input.Limit+1)
	if err != nil {
		return EmployeePage{}, err
	}
	defer rows.Close()
	items := make([]Employee, 0, input.Limit+1)
	for rows.Next() {
		item, err := scanEmployee(rows)
		if err != nil {
			return EmployeePage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return EmployeePage{}, err
	}
	page := EmployeePage{Employees: items}
	if len(items) > input.Limit {
		items = items[:input.Limit]
		page.Employees = items
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(items[len(items)-1].ID, 10)))
	}
	return page, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

// UpdateProfile changes employee identity fields without touching account funds
// or employment status. Existing uniqueness constraints remain authoritative.
func (s *Service) UpdateProfile(ctx context.Context, actorID, id int64, input CreateInput) (Employee, error) {
	input.EmployeeNo = strings.TrimSpace(input.EmployeeNo)
	input.Name = strings.TrimSpace(input.Name)
	input.Phone = normalizePhone(input.Phone)
	input.Department = strings.TrimSpace(input.Department)
	input.PhotoURL = strings.TrimSpace(input.PhotoURL)
	if !employeeNoPattern.MatchString(input.EmployeeNo) || len(input.Name) < 1 || len(input.Name) > 100 ||
		!phonePattern.MatchString(input.Phone) || len(input.Department) < 1 || len(input.Department) > 100 || !validPhotoURL(input.PhotoURL) {
		return Employee{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Employee{}, err
	}
	defer tx.Rollback()
	var currentStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM employees WHERE id=?`, id).Scan(&currentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Employee{}, ErrNotFound
	}
	if err != nil {
		return Employee{}, err
	}
	if currentStatus == "CLOSED" {
		return Employee{}, ErrInvalidState
	}
	result, err := tx.ExecContext(ctx, `UPDATE employees SET employee_no=?,name=?,phone=?,department=?,photo_url=?,updated_at=? WHERE id=? AND status!='CLOSED'`, input.EmployeeNo, input.Name, input.Phone, input.Department, nullString(input.PhotoURL), time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return Employee{}, classifyWrite(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Employee{}, err
	}
	if count == 0 {
		return Employee{}, ErrNotFound
	}
	if err := store.RecordAudit(ctx, tx, actorID, "EMPLOYEE_PROFILE_UPDATED", "employee", strconv.FormatInt(id, 10), nil); err != nil {
		return Employee{}, err
	}
	if err := tx.Commit(); err != nil {
		return Employee{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) SetStatus(ctx context.Context, actorID, id int64, target string) (Employee, error) {
	if target != "ACTIVE" && target != "FROZEN" && target != "CLOSED" {
		return Employee{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Employee{}, err
	}
	defer tx.Rollback()
	var current, accountStatus string
	var balance int64
	err = tx.QueryRowContext(ctx, `SELECT e.status, a.status, a.balance FROM employees e
		JOIN accounts a ON a.employee_id = e.id WHERE e.id = ?`, id).Scan(&current, &accountStatus, &balance)
	if errors.Is(err, sql.ErrNoRows) {
		return Employee{}, ErrNotFound
	}
	if err != nil {
		return Employee{}, err
	}
	if current != accountStatus {
		return Employee{}, ErrInvalidState
	}
	if current != target {
		if current == "CLOSED" || (target == "CLOSED" && balance != 0) {
			return Employee{}, ErrInvalidState
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE employees SET status = ?, updated_at = ? WHERE id = ?`, target, now, id); err != nil {
			return Employee{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET status = ?, updated_at = ? WHERE employee_id = ?`, target, now, id); err != nil {
			return Employee{}, err
		}
		if target == "CLOSED" {
			if _, err := RevokeSessions(ctx, tx, id); err != nil {
				return Employee{}, err
			}
		}
		if err := store.RecordAudit(ctx, tx, actorID, "EMPLOYEE_STATUS_CHANGED", "employee", strconv.FormatInt(id, 10),
			map[string]string{"from": current, "to": target}); err != nil {
			return Employee{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Employee{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) ResetPassword(ctx context.Context, actorID, id int64) (string, error) {
	temporaryPassword, err := newTemporaryPassword()
	if err != nil {
		return "", err
	}
	hash, err := adminauth.HashPassword(temporaryPassword)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE employees SET password_hash = ?, must_change_password = 1, updated_at = ?
		WHERE id = ? AND status != 'CLOSED'`, hash, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", ErrNotFound
	}
	if _, err := RevokeSessions(ctx, tx, id); err != nil {
		return "", err
	}
	if err := store.RecordAudit(ctx, tx, actorID, "EMPLOYEE_PASSWORD_RESET", "employee", strconv.FormatInt(id, 10), nil); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return temporaryPassword, nil
}

// RevokeSessions is usable by later account and payment operations in their
// transaction. Payment tokens must check their issuing session before use.
func RevokeSessions(ctx context.Context, tx *sql.Tx, employeeID int64) (int64, error) {
	result, err := tx.ExecContext(ctx, `UPDATE employee_sessions SET revoked_at = ?
		WHERE employee_id = ? AND revoked_at IS NULL`, time.Now().Unix(), employeeID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type scanner interface{ Scan(dest ...any) error }

func scanEmployee(row scanner) (Employee, error) {
	var item Employee
	var photo sql.NullString
	var mustChange int
	err := row.Scan(&item.ID, &item.AccountID, &item.EmployeeNo, &item.Name, &item.Phone, &item.Department,
		&photo, &item.Status, &item.AccountStatus, &item.Balance, &mustChange)
	if err != nil {
		return Employee{}, err
	}
	if photo.Valid {
		item.PhotoURL = &photo.String
	}
	item.MustChangePassword = mustChange == 1
	return item, nil
}

func normalizePhone(phone string) string {
	phone = strings.TrimSpace(phone)
	return strings.TrimPrefix(phone, "+86")
}

func validPhotoURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2048 {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func newTemporaryPassword() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func pointerIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func classifyWrite(err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
		return ErrConflict
	}
	return fmt.Errorf("write employee: %w", err)
}
