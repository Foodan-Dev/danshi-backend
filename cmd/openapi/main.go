// Command openapi 生成 api/openapi.json。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/hlog"

	"github.com/jingyijun/danshi_backend_go/internal/apicontract"
	"github.com/jingyijun/danshi_backend_go/internal/codegen/apierrcodes"
	openapigen "github.com/jingyijun/danshi_backend_go/internal/openapi"
	"github.com/jingyijun/danshi_backend_go/internal/router"
)

func main() {
	hlog.SetOutput(io.Discard)
	output := flag.String("output", "api/openapi.json", "输出文件；使用 - 写到 stdout")
	codesInput := flag.String("codes", "internal/apierr/codes.go", "错误码唯一清单")
	check := flag.Bool("check", false, "只检查输出文件是否与重新生成的内容完全一致")
	flag.Parse()

	encoded, err := generateSpec(*codesInput)
	if err != nil {
		fail(err)
	}
	if err := writeOrCheck(*output, encoded, *check); err != nil {
		fail(err)
	}
}

func generateSpec(codesInput string) ([]byte, error) {
	catalog, err := apierrcodes.Parse(codesInput)
	if err != nil {
		return nil, err
	}
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	router.Register(engine, router.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return openapigen.Generate(
		engine.Routes(), apicontract.Routes(), router.OpenAPIBindings(),
		openapigen.CodeCatalog{
			FieldCodes: catalog.FieldCodes,
			BizCodes:   catalog.BizCodes,
		},
	)
}

func writeOrCheck(output string, encoded []byte, check bool) error {
	if output == "-" {
		if check {
			return fmt.Errorf("-check 不能与 -output - 同时使用")
		}
		if _, err := os.Stdout.Write(encoded); err != nil {
			return err
		}
		return nil
	}
	if check {
		current, err := os.ReadFile(output)
		if err != nil {
			return fmt.Errorf("读取已提交 spec: %w", err)
		}
		if !bytes.Equal(current, encoded) {
			return fmt.Errorf("%s 已漂移；请运行 make openapi-generate 并提交结果", output)
		}
		return nil
	}
	return os.WriteFile(output, encoded, 0o644)
}

func fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "openapi: %v\n", err)
	os.Exit(1)
}
