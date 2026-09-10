package service

import (
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/passwordx"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/usernamepolicy"
)

const (
	minUsernameRunes = 2
	maxUsernameRunes = 24
)

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || strings.Count(email, "@") != 1 || len(email) > 255 {
		return "", apierr.InvalidField("email", apierr.FieldInvalidFormat, "邮箱格式不正确")
	}
	return email, nil
}

func emailDomain(email string) string {
	_, domain, _ := strings.Cut(email, "@")
	return domain
}

func validatePassword(password string, registering bool) error {
	return validatePasswordField("password", password, registering)
}

func validatePasswordField(field, password string, registering bool) error {
	if password == "" {
		return apierr.InvalidField(field, apierr.FieldRequired, "密码不能为空")
	}
	if !registering {
		return nil
	}
	length := utf8.RuneCountInString(password)
	if length < 8 {
		return apierr.InvalidField(field, apierr.FieldTooShort, "密码不能少于 8 个字符")
	}
	if length > 64 || len(password) > passwordx.MaxLen {
		return apierr.InvalidField(field, apierr.FieldTooLong, "密码不能超过 64 个字符且不能超过 72 字节")
	}
	return nil
}

func normalizeRegister(input RegisterInput) (RegisterInput, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return RegisterInput{}, err
	}
	input.Email = email
	if err := validatePassword(input.Password, true); err != nil {
		return RegisterInput{}, err
	}
	if input.Username == nil {
		return RegisterInput{}, apierr.InvalidField("username", apierr.FieldRequired, "用户名不能为空")
	}
	name, err := normalizeUsername(*input.Username)
	if err != nil {
		return RegisterInput{}, err
	}
	input.Username = &name
	if input.Gender != nil && !validGender(model.Gender(*input.Gender)) {
		return RegisterInput{}, apierr.InvalidField(
			"gender", apierr.FieldInvalidEnum, "gender 只能是 male、female 或 other",
		)
	}
	return input, nil
}

func normalizeUsername(raw string) (string, error) {
	name := strings.TrimSpace(norm.NFKC.String(raw))
	length := utf8.RuneCountInString(name)
	if length == 0 {
		return "", apierr.InvalidField("username", apierr.FieldRequired, "用户名不能为空")
	}
	if length < minUsernameRunes {
		return "", apierr.InvalidField("username", apierr.FieldTooShort, "用户名不能少于 2 个字符")
	}
	if length > maxUsernameRunes {
		return "", apierr.InvalidField("username", apierr.FieldTooLong, "用户名不能超过 24 个字符")
	}
	if !usernamepolicy.ValidCharacters(name) {
		return "", apierr.InvalidField("username", apierr.FieldInvalidFormat, "用户名只能包含文字、数字和下划线")
	}
	if reservedUsername(name) {
		return "", apierr.InvalidField("username", apierr.FieldInvalidFormat, "该用户名不可使用")
	}
	return name, nil
}

func reservedUsername(name string) bool {
	base := strings.ToLower(name)
	for _, reserved := range []string{"admin", "administrator", "official", "support", "system", "security", "danshi"} {
		if base == reserved {
			return true
		}
	}
	return false
}

func validGender(value model.Gender) bool {
	return value == model.GenderMale || value == model.GenderFemale || value == model.GenderOther
}

func validateVerificationCode(code *string) error {
	if code == nil || *code == "" {
		return apierr.InvalidField("verification_code", apierr.FieldRequired, "验证码不能为空")
	}
	if len(*code) != 6 {
		return apierr.InvalidField("verification_code", apierr.FieldInvalidFormat, "验证码必须是 6 位数字")
	}
	for _, digit := range *code {
		if digit < '0' || digit > '9' {
			return apierr.InvalidField("verification_code", apierr.FieldInvalidFormat, "验证码必须是 6 位数字")
		}
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func optionalString(value string, limit int) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	value = truncateRunes(value, limit)
	return &value
}
