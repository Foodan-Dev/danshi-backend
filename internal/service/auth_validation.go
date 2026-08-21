package service

import (
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/passwordx"
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
	if password == "" {
		return apierr.InvalidField("password", apierr.FieldRequired, "密码不能为空")
	}
	if !registering {
		return nil
	}
	length := utf8.RuneCountInString(password)
	if length < 8 {
		return apierr.InvalidField("password", apierr.FieldTooShort, "密码不能少于 8 个字符")
	}
	if length > 64 || len(password) > passwordx.MaxLen {
		return apierr.InvalidField("password", apierr.FieldTooLong, "密码不能超过 64 个字符且不能超过 72 字节")
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
	if input.Name != nil && utf8.RuneCountInString(*input.Name) > 100 {
		return RegisterInput{}, apierr.InvalidField("name", apierr.FieldTooLong, "昵称不能超过 100 个字符")
	}
	if input.Gender != nil && *input.Gender != string(model.GenderMale) && *input.Gender != string(model.GenderFemale) {
		return RegisterInput{}, apierr.InvalidField(
			"gender", apierr.FieldInvalidEnum, "gender 只能是 male 或 female",
		)
	}
	return input, nil
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
