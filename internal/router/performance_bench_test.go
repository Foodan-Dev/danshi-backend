package router_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

func BenchmarkPostListEndToEnd(b *testing.B) {
	if testing.Short() {
		b.Skip("端到端帖子列表基准需要 PostgreSQL testcontainer")
	}
	harness := testutil.NewHarness(b)
	author := harness.Fixtures.CreateActor(harness.Config)
	for index := range 120 {
		harness.Fixtures.CreatePost(author.User.ID,
			testutil.WithPostTitle(fmt.Sprintf("端到端列表基准帖子 %03d", index)))
	}

	for _, pageSize := range []int{10, 20, 50, 100} {
		b.Run(fmt.Sprintf("page_size_%d", pageSize), func(b *testing.B) {
			path := fmt.Sprintf("/api/v2/posts?page=1&limit=%d&sort_by=latest", pageSize)
			status, _, recorder, err := performJSONRequest(
				harness.Engine, http.MethodGet, path, nil, author.Token,
			)
			if err != nil || status != http.StatusOK {
				b.Fatalf("warm-up request failed: status=%d err=%v", status, err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(recorder.Result().Body())))
			b.ResetTimer()
			for b.Loop() {
				status, _, _, err = performJSONRequest(
					harness.Engine, http.MethodGet, path, nil, author.Token,
				)
				if err != nil || status != http.StatusOK {
					b.Fatalf("request failed: status=%d err=%v", status, err)
				}
			}
		})
	}
}
