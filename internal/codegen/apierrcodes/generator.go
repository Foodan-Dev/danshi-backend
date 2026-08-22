// Package apierrcodes 从 apierr/codes.go 的常量声明解析错误码目录。
package apierrcodes

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
)

const (
	fieldCodeType = "FieldCode"
	bizCodeType   = "BizCode"
)

type code struct {
	name  string
	value string
}

// Catalog 是 codes.go 中按声明顺序排列的完整错误码目录。
type Catalog struct {
	FieldCodes []string
	BizCodes   []string
}

// Parse 读取 codes.go，并按声明顺序返回 FieldCode 与 BizCode 的完整目录。
func Parse(input string) (Catalog, error) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, input, nil, 0)
	if err != nil {
		return Catalog{}, fmt.Errorf("解析错误码源文件: %w", err)
	}
	fieldCodes, bizCodes, err := collectCodes(fileSet, parsed)
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{
		FieldCodes: codeValues(fieldCodes),
		BizCodes:   codeValues(bizCodes),
	}, nil
}

func collectCodes(fileSet *token.FileSet, file *ast.File) ([]code, []code, error) {
	definitions := make(map[*ast.Ident]types.Object)
	configuration := types.Config{}
	if _, err := configuration.Check("apierr", fileSet, []*ast.File{file}, &types.Info{Defs: definitions}); err != nil {
		return nil, nil, fmt.Errorf("类型检查错误码源文件: %w", err)
	}
	var fieldCodes, bizCodes []code
	seen := make(map[string]map[string]string)
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, rawSpec := range general.Specs {
			spec, ok := rawSpec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range spec.Names {
				constantObject, ok := definitions[name].(*types.Const)
				if !ok {
					continue
				}
				typeName := namedTypeName(constantObject.Type())
				if typeName != fieldCodeType && typeName != bizCodeType {
					if strings.HasPrefix(name.Name, "Field") || strings.HasPrefix(name.Name, "Biz") {
						return nil, nil, fmt.Errorf("%s 必须显式声明为 FieldCode 或 BizCode", name.Name)
					}
					continue
				}
				if constantObject.Val().Kind() != constant.String {
					return nil, nil, fmt.Errorf("%s.%s 的值必须是非空字符串", typeName, name.Name)
				}
				value := constant.StringVal(constantObject.Val())
				if value == "" {
					return nil, nil, fmt.Errorf("%s.%s 的值必须是非空字符串", typeName, name.Name)
				}
				if err := rememberCode(typeName, name.Name, value, seen); err != nil {
					return nil, nil, err
				}
				item := code{name: name.Name, value: value}
				if typeName == fieldCodeType {
					fieldCodes = append(fieldCodes, item)
				} else {
					bizCodes = append(bizCodes, item)
				}
			}
		}
	}
	if len(fieldCodes) == 0 || len(bizCodes) == 0 {
		return nil, nil, fmt.Errorf("codes.go 必须同时声明 FieldCode 与 BizCode 常量")
	}
	return fieldCodes, bizCodes, nil
}

func namedTypeName(valueType types.Type) string {
	named, ok := valueType.(*types.Named)
	if !ok {
		return ""
	}
	return named.Obj().Name()
}

func rememberCode(typeName, name, value string, seen map[string]map[string]string) error {
	if seen[typeName] == nil {
		seen[typeName] = make(map[string]string)
	}
	if previous := seen[typeName][value]; previous != "" {
		return fmt.Errorf("%s.%s 与 %s 的值 %q 重复", typeName, name, previous, value)
	}
	seen[typeName][value] = name
	return nil
}

func codeValues(codes []code) []string {
	values := make([]string, 0, len(codes))
	for _, item := range codes {
		values = append(values, item.value)
	}
	return values
}
