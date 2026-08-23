package apicontract

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

// QueryField 是从 query DTO 的 query tag 展开的单个参数字段。
type QueryField struct {
	Name     string
	Required bool
	Type     reflect.Type
	Tag      reflect.StructTag
	Index    []int
}

// QueryFields 展开 query DTO。嵌套结构体只负责分组，叶子字段必须显式带 query tag。
func QueryFields(query any) ([]QueryField, error) {
	if query == nil {
		return nil, fmt.Errorf("query 契约未声明")
	}
	queryType := reflect.TypeOf(query)
	for queryType.Kind() == reflect.Pointer {
		queryType = queryType.Elem()
	}
	if queryType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("query 契约必须是结构体，实际为 %s", queryType)
	}

	fields := make([]QueryField, 0, queryType.NumField())
	if err := appendQueryFields(queryType, nil, &fields); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, exists := seen[field.Name]; exists {
			return nil, fmt.Errorf("query 参数 %q 重复声明", field.Name)
		}
		seen[field.Name] = struct{}{}
	}
	return fields, nil
}

func appendQueryFields(queryType reflect.Type, parentIndex []int, fields *[]QueryField) error {
	for index := range queryType.NumField() {
		field := queryType.Field(index)
		fieldIndex := append(append([]int(nil), parentIndex...), field.Index...)
		tag, tagged := field.Tag.Lookup("query")
		if tagged {
			name, required, err := parseQueryTag(tag)
			if err != nil {
				return fmt.Errorf("query 字段 %s.%s: %w", queryType, field.Name, err)
			}
			*fields = append(*fields, QueryField{
				Name: name, Required: required, Type: field.Type, Tag: field.Tag, Index: fieldIndex,
			})
			continue
		}

		if field.PkgPath != "" {
			return fmt.Errorf("query 字段 %s.%s 未导出", queryType, field.Name)
		}
		fieldType := field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() != reflect.Struct {
			return fmt.Errorf("query 字段 %s.%s 缺少 query tag", queryType, field.Name)
		}
		if err := appendQueryFields(fieldType, fieldIndex, fields); err != nil {
			return err
		}
	}
	return nil
}

func parseQueryTag(tag string) (string, bool, error) {
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" || !validQueryName(name) {
		return "", false, fmt.Errorf("非法 query tag %q", tag)
	}
	required := false
	for _, option := range parts[1:] {
		switch option {
		case "required":
			required = true
		case "":
			return "", false, fmt.Errorf("非法 query tag %q", tag)
		default:
			return "", false, fmt.Errorf("未知 query tag 选项 %q", option)
		}
	}
	return name, required, nil
}

func validQueryName(name string) bool {
	for index, r := range name {
		if unicode.IsLetter(r) || r == '_' || index > 0 && unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}
