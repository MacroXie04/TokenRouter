package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

type mockMailer struct {
	sent []struct{ to, subject, body string }
}

func (m *mockMailer) Send(to, subject, body string) error {
	m.sent = append(m.sent, struct{ to, subject, body string }{to, subject, body})
	return nil
}

func initMailDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.AuthFlow{}, &model.UserSession{}))
	model.DB = db
	model.LOG_DB = db
}

func TestPasswordResetFlow(t *testing.T) {
	initMailDB(t)
	prev := Mail
	defer func() { Mail = prev }()
	mock := &mockMailer{}
	Mail = mock

	u := newUser(t, 100)
	require.NoError(t, model.DB.Model(u).Update("email", "user@example.com").Error)

	require.NoError(t, SendPasswordResetEmail("user@example.com"))
	require.Len(t, mock.sent, 1)

	// The code is stored as the auth-flow payload.
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ? AND user_id = ?", PasswordResetPurpose, u.Id).First(&flow).Error)
	code := flow.Payload
	assert.NotEmpty(t, code)

	// Reset with the code.
	require.NoError(t, ResetPassword("user@example.com", code, "newpass123"))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.True(t, common.PasswordVerify("newpass123", got.Password), "password must be updated")

	// Reusing the code fails (consumed).
	assert.Error(t, ResetPassword("user@example.com", code, "anotherpass1"))
}

func TestResetPasswordUnknownEmail(t *testing.T) {
	initMailDB(t)
	err := SendPasswordResetEmail("nobody@example.com")
	assert.Equal(t, ErrUserNotFoundByEmail, err)
}

func TestResetPasswordWrongCode(t *testing.T) {
	initMailDB(t)
	u := newUser(t, 100)
	require.NoError(t, model.DB.Model(u).Update("email", "user@example.com").Error)
	require.NoError(t, SendPasswordResetEmail("user@example.com"))
	assert.Error(t, ResetPassword("user@example.com", "000000", "newpass123"))
}

func TestEmailVerificationFlow(t *testing.T) {
	initMailDB(t)
	prev := Mail
	defer func() { Mail = prev }()
	mock := &mockMailer{}
	Mail = mock

	u := newUser(t, 0)
	require.NoError(t, SendEmailVerificationCode("new@example.com"))
	require.Len(t, mock.sent, 1)

	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", EmailVerificationPurpose).First(&flow).Error)
	code := flow.Payload
	assert.NotEmpty(t, code)

	require.NoError(t, VerifyAndBindEmail(u.Id, "new@example.com", code))
	assert.True(t, UserEmailVerified(u.Id))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, "new@example.com", got.Email)
	assert.True(t, got.EmailVerified)

	// Single-use: reusing the code fails.
	assert.Error(t, VerifyAndBindEmail(u.Id, "other@example.com", code))
}
