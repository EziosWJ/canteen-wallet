package acceptance

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"
)

func TestEmployeePasswordsAcceptSixCharactersAndSimpleResetPasswords(t *testing.T) {
	e := newEnv(t)
	var created struct {
		Employee struct {
			ID int64 `json:"id"`
		} `json:"employee"`
		TemporaryPassword string `json:"temporary_password"`
	}
	e.post(e.public.URL, "/api/admin/employees", e.adminToken, map[string]string{
		"employee_no": "PASS-001", "name": "密码测试", "phone": "13900000881", "department": "食堂",
	}).expect(t, http.StatusCreated, &created)
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(created.TemporaryPassword) {
		t.Fatalf("initial employee password %q is not six digits", created.TemporaryPassword)
	}

	var initialLogin struct {
		AccessToken string `json:"access_token"`
	}
	e.post(e.public.URL, "/api/auth/login", "", map[string]string{
		"phone": "13900000881", "password": created.TemporaryPassword,
	}).expect(t, http.StatusOK, &initialLogin)

	path := fmt.Sprintf("/api/admin/employees/%d/reset-password", created.Employee.ID)
	e.post(e.public.URL, path, e.adminToken, map[string]string{"password": "12345"}).expect(t, http.StatusBadRequest, nil)
	var manualReset struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	e.post(e.public.URL, path, e.adminToken, map[string]string{"password": "123456"}).expect(t, http.StatusOK, &manualReset)
	if manualReset.TemporaryPassword != "123456" {
		t.Fatalf("manual reset returned password %q", manualReset.TemporaryPassword)
	}
	if response := e.get(e.public.URL, "/api/me", initialLogin.AccessToken); response.status != http.StatusUnauthorized {
		t.Fatalf("password reset left the prior session active: status=%d body=%s", response.status, response.body)
	}

	var manualLogin struct {
		AccessToken        string `json:"access_token"`
		MustChangePassword bool   `json:"must_change_password"`
	}
	e.post(e.public.URL, "/api/auth/login", "", map[string]string{
		"phone": "13900000881", "password": manualReset.TemporaryPassword,
	}).expect(t, http.StatusOK, &manualLogin)
	if !manualLogin.MustChangePassword {
		t.Fatal("manual reset password did not require a change at first login")
	}
	// Six Unicode characters with no language or character-class requirements.
	newPassword := "员工密码66"
	e.post(e.public.URL, "/api/me/change-password", manualLogin.AccessToken, map[string]string{
		"old_password": manualReset.TemporaryPassword, "new_password": newPassword,
	}).expect(t, http.StatusOK, nil)
	e.post(e.public.URL, "/api/auth/login", "", map[string]string{
		"phone": "13900000881", "password": newPassword,
	}).expect(t, http.StatusOK, nil)

	var generatedReset struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	e.post(e.public.URL, path, e.adminToken, map[string]string{}).expect(t, http.StatusOK, &generatedReset)
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(generatedReset.TemporaryPassword) {
		t.Fatalf("generated reset password %q is not six digits", generatedReset.TemporaryPassword)
	}
}
