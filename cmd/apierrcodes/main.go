// Command apierrcodes 从 internal/apierr/codes.go 生成错误码目录。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jingyijun/danshi_backend_go/internal/codegen/apierrcodes"
)

func main() {
	input := flag.String("input", "internal/apierr/codes.go", "错误码唯一清单")
	output := flag.String("output", "internal/apierr/codes_gen.go", "生成的错误码目录")
	check := flag.Bool("check", false, "只检查生成物是否最新")
	flag.Parse()
	if err := apierrcodes.Write(*input, *output, *check); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "apierrcodes: %v\n", err)
		os.Exit(1)
	}
}
