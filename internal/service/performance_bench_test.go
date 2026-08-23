package service

import (
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

var benchmarkSnapshot postSnapshot

func BenchmarkSnapshotFromCurrent(b *testing.B) {
	shareType := model.ShareTypeRecommend
	canteenID, windowID, cuisineID := uint64(11), uint64(12), uint64(13)
	post := model.Post{
		ID: 1, PostType: model.PostTypeShare, ShareType: &shareType,
		Category: model.PostCategoryFood,
		Title:    "旧版本快照基准标题", Content: "旧版本快照基准正文",
		CanteenID: &canteenID, CanteenWindowID: &windowID, CuisineID: &cuisineID,
	}
	relations := repository.PostRelations{
		Tags: map[uint64][]string{post.ID: {"午餐", "食堂", "性价比"}},
		Flavors: map[uint64][]repository.PostFlavorRow{
			post.ID: {
				{PostID: post.ID, Name: "微辣", Stance: model.FlavorStanceHas},
				{PostID: post.ID, Name: "鲜香", Stance: model.FlavorStanceHas},
				{PostID: post.ID, Name: "清淡", Stance: model.FlavorStanceHas},
			},
		},
		Images: map[uint64][]string{
			post.ID: {
				"https://images.example.test/posts/a.webp",
				"https://images.example.test/posts/b.webp",
			},
		},
	}

	b.ReportAllocs()
	for b.Loop() {
		benchmarkSnapshot = snapshotFromCurrent(&post, relations)
	}
}
