package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
)

var benchmarkSnapshot postSnapshot

func BenchmarkBuildSnapshot(b *testing.B) {
	price, err := money.Parse("18.50")
	if err != nil {
		b.Fatal(err)
	}
	shareType := model.ShareTypeRecommend
	canteenID, windowID, cuisineID := uint64(11), uint64(12), uint64(13)
	resolved := resolvedPostPayload{
		normalizedPostPayload: normalizedPostPayload{
			PostType: model.PostTypeShare, ShareType: &shareType,
			Category: model.PostCategoryFood,
			Title:    "审核快照基准标题", Content: "审核快照基准正文",
			Price:       &price,
			FlavorNames: []string{"微辣", "鲜香", "清淡"},
			FlavorStances: map[string]model.FlavorStance{
				"微辣": model.FlavorStanceHas, "鲜香": model.FlavorStanceHas,
				"清淡": model.FlavorStanceHas,
			},
			Tags: []string{"午餐", "食堂", "性价比"},
			Images: []string{
				"https://images.example.test/posts/a.webp",
				"https://images.example.test/posts/b.webp",
			},
			Publish: true,
		},
		CanteenID: &canteenID, CuisineID: &cuisineID,
		Flavors: []model.Flavor{
			{ID: 3, Name: "清淡", SortOrder: 30},
			{ID: 1, Name: "微辣", SortOrder: 10},
			{ID: 2, Name: "鲜香", SortOrder: 20},
		},
	}
	resolved.CanteenWindowID = &windowID

	b.ReportAllocs()
	for b.Loop() {
		benchmarkSnapshot = buildSnapshot(resolved)
	}
}

func BenchmarkAssertCurrentMatchesLatest(b *testing.B) {
	if testing.Short() {
		b.Skip("审核快照一致性比较需要 PostgreSQL testcontainer")
	}
	gdb, database := openSnapshotBenchmarkDatabase(b)
	user := model.User{
		Email: "snapshot-benchmark@fdueat.com", PasswordHash: "benchmark-hash",
		Name: "快照基准用户",
	}
	if err := gdb.Create(&user).Error; err != nil {
		b.Fatal(err)
	}
	shareType := model.ShareTypeRecommend
	post := model.Post{
		AuthorID: user.ID, PostType: model.PostTypeShare, ShareType: &shareType,
		Status: model.PostStatusApproved, Category: model.PostCategoryFood,
		Title: "快照比较标题", Content: "快照比较正文",
	}
	if err := gdb.Create(&post).Error; err != nil {
		b.Fatal(err)
	}
	tag := model.Tag{Name: "基准标签", Moderation: model.ModerationStatusPass}
	flavor := model.Flavor{Name: "基准口味", IsActive: true}
	size := int64(2048)
	image := model.ImageAsset{
		UploaderID: &user.ID, Purpose: model.ImagePurposePost,
		ObjectKey: "benchmark/snapshot.webp", PublicURL: "https://images.example.test/snapshot.webp",
		ContentType: "image/webp", Size: &size, Status: model.ImageStatusReady,
		Moderation: model.ModerationStatusPass,
	}
	for _, value := range []any{&tag, &flavor, &image} {
		if err := gdb.Create(value).Error; err != nil {
			b.Fatal(err)
		}
	}
	for _, value := range []any{
		&model.PostTag{PostID: post.ID, TagID: tag.ID},
		&model.PostFlavor{
			PostID: post.ID, FlavorID: flavor.ID,
			Stance: model.FlavorStanceHas, PostType: model.PostTypeShare,
		},
		&model.PostImage{PostID: post.ID, Position: 0, ImageAssetID: image.ID},
	} {
		if err := gdb.Create(value).Error; err != nil {
			b.Fatal(err)
		}
	}
	relations := repository.PostRelations{
		Tags: map[uint64][]string{post.ID: {tag.Name}},
		Flavors: map[uint64][]repository.PostFlavorRow{
			post.ID: {{PostID: post.ID, Name: flavor.Name, Stance: model.FlavorStanceHas}},
		},
		Images: map[uint64][]string{post.ID: {image.PublicURL}},
	}
	snapshot, err := json.Marshal(snapshotFromCurrent(&post, relations))
	if err != nil {
		b.Fatal(err)
	}
	latest := model.PostHistory{PostID: post.ID, Revision: 1, Snapshot: snapshot}
	postService := NewPostService(DirectPassContentModerator{})

	b.ReportAllocs()
	err = database.RunInTx(context.Background(), func(ctx context.Context) error {
		b.ResetTimer()
		for b.Loop() {
			if compareErr := postService.assertCurrentMatchesLatest(ctx, &post, &latest); compareErr != nil {
				return compareErr
			}
		}
		b.StopTimer()
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
}

func openSnapshotBenchmarkDatabase(b *testing.B) (*gorm.DB, *dbinfra.DB) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	b.Cleanup(cancel)
	container, err := tcpostgres.Run(
		ctx,
		"postgres:18",
		tcpostgres.WithDatabase("danshi_snapshot_benchmark"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("benchmark"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		b.Fatal(err)
	}
	testcontainers.CleanupContainer(b, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatal(err)
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			b.Errorf("close benchmark database: %v", closeErr)
		}
	})
	if err := dbinfra.Up(ctx, sqlDB); err != nil {
		b.Fatal(err)
	}
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		SkipDefaultTransaction:                   true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatal(err)
	}
	return gdb, &dbinfra.DB{DB: gdb}
}
